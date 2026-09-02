.PHONY: bootstrap generate generate-check fmt fmt-check lint test-unit test-race test-contracts test-integration test-e2e test-protocol test-fault verify-license verify-implementation-plan verify-contracts preflight ci build image

SHELL := pwsh

bootstrap:
	pwsh -NoProfile -File tools/dev/bootstrap.ps1

generate:
	go run ./tools/codegen

generate-check:
	go run ./tools/codegen -check

fmt:
	pwsh -NoProfile -File tools/dev/format.ps1

fmt-check:
	pwsh -NoProfile -File tools/dev/format.ps1 -Check

lint:
	go vet ./...

test-unit:
	go test ./... -count=1
	pnpm --dir web test

test-race:
	pwsh -NoProfile -File tools/dev/test-race.ps1

test-contracts:
	go run ./tools/contractcheck

test-integration:
	go test ./internal/app ./internal/dispatchflow ./internal/resourcesapi ./internal/rssservice ./internal/syncwatcher -count=1

test-e2e:
	go test ./cmd/vertex-pro ./internal/httpapi ./internal/resourcesapi ./internal/sseserver -count=1

test-protocol:
	go test ./internal/qui/... ./internal/qbadd ./internal/qbprofile ./internal/qbresponse ./internal/syncmerge -count=1

test-fault:
	go test ./internal/addfaultmatrix ./internal/dispatchcommit ./internal/qbexecutor ./internal/saferetry ./internal/startuprecover ./internal/unknownreconciler -count=1

verify-license:
	pwsh -NoProfile -File tools/licensecheck/verify-license.ps1

verify-implementation-plan:
	pwsh -NoProfile -File tools/dev/verify-implementation-plan.ps1

verify-contracts:
	go run ./tools/contractcheck

preflight:
	pwsh -NoProfile -File tools/dev/preflight.ps1

ci: preflight

build:
	pwsh -NoProfile -File tools/dev/build.ps1

image:
	docker build -f deploy/docker/Dockerfile --build-arg GOPROXY=https://goproxy.cn,direct -t vertex-pro:local .



