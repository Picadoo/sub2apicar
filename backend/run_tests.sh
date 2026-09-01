#!/bin/bash
set -x
cd /d/sub2apicar/sub2api/backend
export GOTOOLCHAIN=local
go test ./internal/service/ -run TestContentModerationRuntimeSnapshotRefreshFailureKeepsStaleConfig -count=3 2>&1 | tail -3
go test ./internal/service/ -run 'TestAccountWindowPool|TestCheckUserAccountEligible|TestCheckEligible|TestSetDonateFraction' -count=1 -v 2>&1 | grep -E '^(--- PASS|--- FAIL|ok|FAIL)'
go test ./internal/handler/ -run 'TestBuildOverviewItems|TestBuildWindowItems|TestAccountWindowQuotaHandler' -count=1 -v 2>&1 | grep -E '^(--- PASS|--- FAIL|ok|FAIL)'
