package service

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIRequestTimezoneKey = "openai_request_timezone"

// An absent/empty override preserves the client's request, including for old accounts.
func (a *Account) OpenAIRequestTimezone() string {
	if a == nil || !a.IsOpenAI() {
		return ""
	}
	name, _ := a.Extra[openAIRequestTimezoneKey].(string)
	if !validOpenAIRequestTimezone(name) {
		return ""
	}
	return name
}

func validOpenAIRequestTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > 100 || strings.TrimSpace(name) != name {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

func ValidateOpenAIRequestTimezoneExtra(platform string, extra map[string]any) error {
	raw, exists := extra[openAIRequestTimezoneKey]
	if !exists || raw == nil {
		return nil
	}
	name, ok := raw.(string)
	if !ok || (name != "" && (platform != PlatformOpenAI || !validOpenAIRequestTimezone(name))) {
		return infraerrors.BadRequest("INVALID_OPENAI_REQUEST_TIMEZONE", "openai_request_timezone must be an IANA timezone for an OpenAI account, or empty to preserve the client timezone")
	}
	return nil
}

// Partial extra updates/re-authorization must not silently clear a saved override.
// Explicit empty string or null clears it; omission retains it.
func preserveOpenAIRequestTimezone(current, next map[string]any) {
	if next == nil {
		return
	}
	if _, provided := next[openAIRequestTimezoneKey]; !provided {
		if value, exists := current[openAIRequestTimezoneKey]; exists {
			next[openAIRequestTimezoneKey] = value
		}
	}
}

// Rewrite only a complete, unambiguous environment block. Date, surrounding
// whitespace, language, quoted examples and other content remain untouched.
func rewriteOpenAIEnvironmentTimezone(text, timezone string) string {
	if !strings.HasPrefix(strings.TrimSpace(text), "<environment_context") {
		return text
	}
	d := xml.NewDecoder(strings.NewReader(text))
	depth, count := 0, 0
	start, end := -1, -1
	rootSeen := false
	for {
		offset := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return text
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if rootSeen || t.Name.Local != "environment_context" || t.Name.Space != "" {
					return text
				}
				rootSeen = true
			}
			if depth == 2 && t.Name.Local == "timezone" {
				count++
				start = int(d.InputOffset())
			}
		case xml.EndElement:
			if depth == 2 && t.Name.Local == "timezone" {
				end = offset
			}
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return text
			}
		case xml.Directive, xml.ProcInst:
			return text
		}
	}
	if depth != 0 || count != 1 || start < 0 || end <= start {
		return text
	}
	value := text[start:end]
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "<>&") {
		return text
	}
	left := len(value) - len(strings.TrimLeft(value, " \t\r\n"))
	right := len(strings.TrimRight(value, " \t\r\n"))
	return text[:start+left] + timezone + text[start+right:]
}

// Apply at the outbound boundary, never to canonical replay/failover input.
// sjson's default SetBytes allocates a result and does not mutate the source.
func applyOpenAIRequestTimezone(account *Account, body []byte) []byte {
	timezone := account.OpenAIRequestTimezone()
	if timezone == "" || !gjson.ValidBytes(body) {
		return body
	}
	if kind := gjson.GetBytes(body, "type").String(); kind != "" && kind != "response.create" && kind != "session.update" {
		return body
	}
	set := func(path, value string) {
		if updated, err := sjson.SetBytes(body, path, value); err == nil {
			body = updated
		}
	}
	for _, prefix := range []string{"", "response.", "session."} {
		gjson.GetBytes(body, prefix+"input").ForEach(func(index, item gjson.Result) bool {
			if item.Get("role").String() != "user" {
				return true
			}
			path := prefix + "input." + strconv.FormatInt(index.Int(), 10) + ".content"
			content := item.Get("content")
			rewrite := func(path string, value gjson.Result) {
				if value.Type == gjson.String {
					if next := rewriteOpenAIEnvironmentTimezone(value.String(), timezone); next != value.String() {
						set(path, next)
					}
				}
			}
			if content.Type == gjson.String {
				rewrite(path, content)
			} else if content.IsArray() {
				content.ForEach(func(i, part gjson.Result) bool {
					if part.Get("type").String() == "input_text" {
						rewrite(path+"."+strconv.FormatInt(i.Int(), 10)+".text", part.Get("text"))
					}
					return true
				})
			}
			return true
		})
		gjson.GetBytes(body, prefix+"tools").ForEach(func(index, tool gjson.Result) bool {
			kind := tool.Get("type").String()
			if kind == "web_search" || strings.HasPrefix(kind, "web_search_") {
				if value := tool.Get("user_location.timezone"); value.Type == gjson.String && value.String() != timezone {
					set(prefix+"tools."+strconv.FormatInt(index.Int(), 10)+".user_location.timezone", timezone)
				}
			}
			return true
		})
	}
	return body
}

func openAIRequestTimezoneWSPayload(account *Account, payload map[string]any) any {
	if account.OpenAIRequestTimezone() == "" {
		return payload
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return payload // Preserve the normal writer's error handling.
	}
	return json.RawMessage(applyOpenAIRequestTimezone(account, body))
}
