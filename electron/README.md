# New API Electron desktop app

The desktop package embeds the New API backend, waits for `/readyz`, opens the local web UI, and terminates the backend when the wrapper exits. Windows, macOS, and Linux packages are built from the same revision and version string.

## Development

Run the backend and web development server first:

```bash
# repository root
go run .
make dev-web

# electron/
npm ci
npm run dev-app
```

Development mode expects the backend on port `3000` and the web server on port `5173`.

## Verification and packaging

From `electron/`:

```bash
npm test
npm run build:all
npm run probe -- dist
```

`build:all` installs the locked web and Electron dependencies, builds the web UI and Go backend with one version, runs the Electron lifecycle tests, and packages the current platform. The package probe verifies the unpacked application, embedded backend, licenses, and distributable artifacts. Add `--write-checksums` to emit `dist/SHA256SUMS`.

Artifacts are written to `electron/dist/`:

- macOS: DMG and ZIP
- Windows: NSIS installer and portable EXE
- Linux: AppImage and DEB

The `.github/workflows/electron.yml` matrix runs the same pipeline on all three operating systems.

## Runtime overrides for diagnostics

The wrapper normally chooses the embedded backend automatically. Tests and support probes may set:

- `ELECTRON_BACKEND_PATH`
- `ELECTRON_BACKEND_PORT`
- `ELECTRON_BACKEND_READY_URL`
- `ELECTRON_READY_ATTEMPTS`
- `ELECTRON_READY_DELAY_MS`
- `ELECTRON_FRONTEND_PORT` (development only)

Production data is stored below Electron's platform-specific `userData/data` directory. Closing the window keeps the tray application alive; choosing Quit or delivering `SIGINT`/`SIGTERM` gracefully stops the backend, with a forced kill after five seconds.
