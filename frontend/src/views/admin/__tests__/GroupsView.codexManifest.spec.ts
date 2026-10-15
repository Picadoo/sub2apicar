import { defineComponent, h, type PropType } from "vue";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGroup, CodexModelsManifestConfig } from "@/types";
import GroupsView from "@/views/admin/GroupsView.vue";

const {
  listGroups,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  updateGroup,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  updateGroup: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      create: vi.fn(),
      update: updateGroup,
      delete: vi.fn(),
      duplicate: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}));

vi.mock("@/stores/onboarding", () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}));

vi.mock("@/stores/auth", () => ({
  useAuthStore: () => ({ isSimpleMode: false }),
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  };
});

const sourceGroup = {
  id: 42,
  name: "OpenAI",
  description: null,
  platform: "openai",
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: "active",
  subscription_type: "standard",
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  long_context_pricing_enabled: true,
  force_openai_fast: false,
  free_openai_fast: false,
  model_pricing: [],
  profit_control_enabled: false,
  profit_min_margin: 0,
  profit_safety_buffer: 0,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  search_price_per_1k: null,
  audio_realtime_price_per_min: null,
  audio_tts_price_per_million_chars: null,
  audio_stt_price_per_hour: null,
  peak_rate_enabled: false,
  peak_start: "",
  peak_end: "",
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  model_allowlist: undefined,
  codex_models_manifest_config: {
    enabled: false,
    account_ids: [],
    fallback_to_scheduler: false,
  },
  sort_order: 10,
} satisfies AdminGroup;

const AppLayoutStub = defineComponent({
  template: "<main><slot /></main>",
});

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
});

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
  },
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-openai_fast" :row="row" /><slot name="cell-actions" :row="row" /></div></div>',
});

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
});

const CodexManifestAccountsFieldStub = defineComponent({
  name: "CodexManifestAccountsField",
  props: {
    modelValue: {
      type: Object as PropType<CodexModelsManifestConfig>,
      required: true,
    },
  },
  emits: ["update:modelValue"],
  setup(props, { emit, expose }) {
    expose({ validate: () => true, resetValidation: () => undefined });
    return () =>
      h("div", { "data-testid": "codex-manifest-field" }, [
        h(
          "output",
          { "data-testid": "codex-manifest-value" },
          JSON.stringify(props.modelValue),
        ),
        h(
          "button",
          {
            type: "button",
            "data-testid": "codex-manifest-enable",
            onClick: () =>
              emit("update:modelValue", {
                ...props.modelValue,
                enabled: true,
              }),
          },
          "enable",
        ),
        h(
          "button",
          {
            type: "button",
            "data-testid": "codex-manifest-select-account",
            onClick: () =>
              emit("update:modelValue", {
                ...props.modelValue,
                account_ids: [17],
              }),
          },
          "select account",
        ),
      ]);
  },
});

const mountView = () =>
  mount(GroupsView, {
    global: {
      plugins: [createPinia()],
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        ReasoningEffortPolicyFields: true,
        CodexManifestAccountsField: CodexManifestAccountsFieldStub,
        PricingEntryCard: true,
        VueDraggable: true,
      },
    },
  });

enableAutoUnmount(afterEach);

describe("GroupsView group controls", () => {
  beforeEach(() => {
    localStorage.clear();
    listGroups.mockReset();
    getModelAllowlistCandidates.mockReset();
    getUsageSummary.mockReset();
    getCapacitySummary.mockReset();
    getLiveCapability.mockReset();
    updateGroup.mockReset();
    showError.mockReset();
    showSuccess.mockReset();

    listGroups.mockResolvedValue({
      items: [structuredClone(sourceGroup)],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    getModelAllowlistCandidates.mockResolvedValue([]);
    getUsageSummary.mockResolvedValue([]);
    getCapacitySummary.mockResolvedValue([]);
    getLiveCapability.mockResolvedValue({ supported: false });
  });

  it("preserves consecutive child updates on the reactive edit config", async () => {
    const wrapper = mountView();
    await flushPromises();

    const editButton = wrapper
      .findAll("button")
      .find((button) => button.text().includes("common.edit"));
    expect(editButton).toBeTruthy();
    await editButton!.trigger("click");
    await flushPromises();

    expect(wrapper.get('[data-testid="codex-manifest-value"]').text()).toBe(
      JSON.stringify(sourceGroup.codex_models_manifest_config),
    );

    await wrapper.get('[data-testid="codex-manifest-enable"]').trigger("click");
    await flushPromises();
    expect(wrapper.get('[data-testid="codex-manifest-value"]').text()).toContain(
      '"enabled":true',
    );

    await wrapper
      .get('[data-testid="codex-manifest-select-account"]')
      .trigger("click");
    await flushPromises();
    expect(wrapper.get('[data-testid="codex-manifest-value"]').text()).toBe(
      JSON.stringify({
        enabled: true,
        account_ids: [17],
        fallback_to_scheduler: false,
      }),
    );

  });

  it("opens and closes group Fast without sending other group settings", async () => {
    const wrapper = mountView();
    await flushPromises();
    const toggle = wrapper.get('[data-testid="group-fast-toggle-42"]');
    expect(toggle.attributes('aria-checked')).toBe('false');

    updateGroup.mockResolvedValueOnce({ ...sourceGroup, force_openai_fast: true });
    await toggle.trigger('click');
    await flushPromises();
    expect(updateGroup).toHaveBeenLastCalledWith(42, { force_openai_fast: true });
    expect(toggle.attributes('aria-checked')).toBe('true');

    updateGroup.mockResolvedValueOnce({ ...sourceGroup, force_openai_fast: false });
    await toggle.trigger('click');
    await flushPromises();
    expect(updateGroup).toHaveBeenLastCalledWith(42, { force_openai_fast: false });
    expect(toggle.attributes('aria-checked')).toBe('false');
    expect(showSuccess).toHaveBeenCalledTimes(2);
  });

  it("keeps the previous state on failure and allows a retry", async () => {
    const wrapper = mountView();
    await flushPromises();
    const toggle = wrapper.get('[data-testid="group-fast-toggle-42"]');
    updateGroup.mockRejectedValueOnce(new Error('save failed'));
    await toggle.trigger('click');
    await flushPromises();
    expect(toggle.attributes('aria-checked')).toBe('false');
    expect(toggle.attributes('disabled')).toBeUndefined();
    expect(showError).toHaveBeenCalledOnce();

    updateGroup.mockResolvedValueOnce({ ...sourceGroup, force_openai_fast: true });
    await toggle.trigger('click');
    await flushPromises();
    expect(toggle.attributes('aria-checked')).toBe('true');
  });

  it("prevents repeated saves and stale edits while a toggle is pending", async () => {
    let resolveUpdate!: (value: AdminGroup) => void;
    updateGroup.mockReturnValueOnce(new Promise<AdminGroup>((resolve) => { resolveUpdate = resolve; }));
    const wrapper = mountView();
    await flushPromises();
    const toggle = wrapper.get('[data-testid="group-fast-toggle-42"]');
    await toggle.trigger('click');
    expect(toggle.attributes('disabled')).toBeDefined();
    const edit = wrapper.findAll('button').find((button) => button.text().includes('common.edit'))!;
    expect(edit.attributes('disabled')).toBeDefined();
    await toggle.trigger('click');
    expect(updateGroup).toHaveBeenCalledOnce();
    resolveUpdate({ ...sourceGroup, force_openai_fast: true });
    await flushPromises();
    expect(toggle.attributes('disabled')).toBeUndefined();
    expect(edit.attributes('disabled')).toBeUndefined();
  });

  it("only shows Fast controls for OpenAI and Composite groups", async () => {
    listGroups.mockResolvedValueOnce({ items: [
      structuredClone(sourceGroup),
      { ...sourceGroup, id: 43, platform: 'composite' },
      { ...sourceGroup, id: 44, platform: 'anthropic' },
    ], total: 3, page: 1, page_size: 20, pages: 1 });
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.find('[data-testid="group-fast-toggle-42"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="group-fast-toggle-43"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="group-fast-toggle-44"]').exists()).toBe(false);
  });
});
