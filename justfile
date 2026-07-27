set shell := ["bash", "-euo", "pipefail", "-c"]

program := 'hetdns'

version := 'SNAPSHOT-'+`git describe --tags --always --dirty 2>/dev/null || printf 'unknown'`
commit_sha := `(git rev-parse --verify HEAD 2>/dev/null || printf 'unknown') | tr -d '\n'`
build_time := `date -u '+%Y-%m-%d_%H:%M:%S'`

container_engine := 'docker'
container_registry := 'ghcr.io'
container_image := container_registry + '/acidghost/' + program

ldflags := '-s -w -X main.buildVersion='+version \
        +' -X main.buildCommit='+commit_sha \
        +' -X main.buildDate='+build_time

main_package := './cmd/hetdns'

install_prefix := `go env GOBIN`

goos := if os() == 'macos' { 'darwin' } else { os() }
goarch := if arch() == 'aarch64' { 'arm64' } else if arch() == 'x86_64' { 'amd64' } else { arch() }

alias b := build
alias r := run

build-all: (build 'darwin' 'arm64') (build 'linux' 'arm64') (build 'linux' 'amd64')

build-image platform=goarch:
    {{container_engine}} build \
        --platform 'linux/{{platform}}' \
        --build-arg BUILD_VERSION='{{version}}' \
        --build-arg BUILD_COMMIT='{{commit_sha}}' \
        -t '{{container_image}}' .

build os=goos arch=goarch: build-dir
    CGO_ENABLED=0 GOOS={{os}} GOARCH={{arch}} \
        go build \
            -trimpath \
            -ldflags '{{ldflags}}' \
            -o build/{{program}}-{{os}}-{{arch}} \
            {{main_package}}

build-dir:
    mkdir -p build

run *args: build
    ./build/{{program}}-{{goos}}-{{goarch}} {{args}}

helm-lint:
    helm lint deploy/charts/{{program}}

helm-conform:
    helm template {{program}} deploy/charts/{{program}} \
        | kubeconform -summary
    helm template {{program}} deploy/charts/{{program}} \
        --set config.existingConfigMap=external-hetdns-config \
        --set-string runtime.listen=:9090 \
        --set ingress.enabled=true \
        --set ingress.className=nginx \
        --set 'ingress.hosts[0].host=hetdns.example.com' \
        --set 'ingress.hosts[0].paths[0].path=/' \
        --set 'ingress.hosts[0].paths[0].pathType=Prefix' \
        --set 'ingress.tls[0].secretName=hetdns-tls' \
        --set 'ingress.tls[0].hosts[0]=hetdns.example.com' \
        | kubeconform -summary

helm-package version='0.0.0': build-dir
    release_version='{{version}}'; release_version="${release_version#version=}"; \
        helm package \
            --destination build \
            --version "$release_version" \
            --app-version "$release_version" \
            deploy/charts/{{program}}

helm-push version:
    release_version='{{version}}'; release_version="${release_version#version=}"; \
        archive="build/{{program}}-${release_version}.tgz"; \
        if [[ ! -f "$archive" ]]; then just helm-package "$release_version"; fi; \
        helm push "$archive" 'oci://{{container_registry}}/acidghost/charts'

actions-lint *args:
    actionlint -verbose {{args}}

vendor:
    go mod tidy
    go mod vendor

fmt:
    golangci-lint fmt

lint:
    golangci-lint run

test:
    go test ./...

test-race:
    go test -race ./...

check: fmt lint test

install: build
    cp -v './build/{{program}}-{{goos}}-{{goarch}}' "{{install_prefix}}/{{program}}"

clean:
    rm -rf build

help:
    @just --list
