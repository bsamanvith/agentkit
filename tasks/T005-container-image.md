---
id: T005
title: Distroless container image
epic: E01
status: todo
depends_on: [T003, T004]
---

# T005: Distroless container image

## Context

[PLAN.md](../docs/PLAN.md) targets 10-20 MB images. One Dockerfile builds every binary.

## Scope

- Multi-stage `Dockerfile`: Go build stage with module and build caches, `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"` + version info; final stage `gcr.io/distroless/static:nonroot`.
- `--build-arg BIN=<harnessd|toolserver|allinone|fakellm>`.
- Health checking without a shell: the binary supports `<bin> healthcheck` (hits its own `/healthz`), used by compose and k8s probes.
- `make image BIN=...` and `make images`.
- `.dockerignore`.

## Out of scope

- Publishing images to a registry (later, with release automation).
- SQLite needs CGO with some drivers: use a pure-Go driver, decided when `store/sqlite` lands. Note it here if it changes the image.

## Acceptance criteria

- [ ] Each image builds, runs as non-root, and is under 20 MB (checked in CI by T008).
- [ ] `docker run <image> healthcheck` exits non-zero when the server is not up and 0 when it is.
- [ ] A rebuild after changing one `.go` file reuses the module download layer.

## Notes

-
