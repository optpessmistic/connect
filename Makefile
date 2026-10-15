# Go 1.27 requires macOS 13+. Keep native compilation and bundle metadata aligned.
ifeq ($(shell uname -s),Darwin)
export MACOSX_DEPLOYMENT_TARGET := 13.0
export CGO_CFLAGS := -mmacosx-version-min=13.0
export CGO_LDFLAGS := -mmacosx-version-min=13.0
endif

.PHONY: install frontend build run preview package-mac icons
icons:
	./scripts/build-icons.sh
install:
	npm --prefix frontend ci
	go mod download
frontend:
	npm --prefix frontend run build
build: frontend
	go build -tags production -trimpath -o bin/surplustoken-connect .
run: build
	./bin/surplustoken-connect
preview:
	npm --prefix frontend run dev
package-mac: icons
	$(MAKE) build
	mkdir -p "bin/SurplusToken Connect.app/Contents/MacOS"
	cp bin/surplustoken-connect "bin/SurplusToken Connect.app/Contents/MacOS/"
	mkdir -p "bin/SurplusToken Connect.app/Contents/Resources"
	cp build/darwin/AppIcon.icns "bin/SurplusToken Connect.app/Contents/Resources/"
	cp build/darwin/Info.plist "bin/SurplusToken Connect.app/Contents/Info.plist"

.PHONY: api-dev test test-e2e
api-dev:
	go run ./cmd/connect-dev
test:
	go test -race ./internal/... ./cmd/...
	npm --prefix frontend test
	npm --prefix frontend run build
test-e2e:
	npm --prefix frontend run test:e2e
