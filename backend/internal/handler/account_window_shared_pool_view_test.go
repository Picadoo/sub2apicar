package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountWindowSharedPoolViewsPreserveUsageAndExposeSharedHeadroom(t *testing.T) {
	official := 80.0
	view := service.UserWindowQuotaView{
		AccountID: 7, WindowType: service.WindowType7d, SharedPoolMode: true,
		LimitPercent: 23, EffectiveLimitPercent: 23, AttributedPercent: 30,
		AccountUsedPercent: &official, CeilingPercent: 92,
	}
	items := buildWindowItemsFromViews([]service.UserWindowQuotaView{view}, time.Now())
	require.True(t, items[0].SharedPoolMode)
	require.Equal(t, 30.0, items[0].UsedPercent)
	window := &keyUsageAccountWindow{}
	applyUserWindowView(window, view)
	require.True(t, window.UserSharedPoolMode)
	require.Equal(t, 12.0, *window.UserRemainingPercent)
	require.Equal(t, 30.0, *window.UserUsedPercent)
	require.Nil(t, window.UserEffectiveLimitPercent)
	view.AccountUsedPercent = nil
	applyUserWindowView(window, view)
	require.Nil(t, window.UserRemainingPercent, "unknown official usage must not invent shared headroom")
}
