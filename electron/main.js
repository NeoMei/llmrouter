const { app, BrowserWindow, dialog, Menu, shell, Tray } = require('electron');
const fs = require('node:fs');
const path = require('node:path');

const { isAllowedNavigation } = require('./navigation-policy');
const { createBackendRuntime, waitForReady } = require('./server-runtime');

const PORT = Number(process.env.ELECTRON_BACKEND_PORT || 3000);
const DEV_FRONTEND_PORT = Number(process.env.ELECTRON_FRONTEND_PORT || 5173);
const READY_URL =
  process.env.ELECTRON_BACKEND_READY_URL ||
  `http://127.0.0.1:${PORT}/readyz`;

let backendRuntime = null;
let mainWindow = null;
let tray = null;
let quitting = false;
let backendReady = false;
const serverErrorLogs = [];

function rememberServerError(message) {
  serverErrorLogs.push(message);
  if (serverErrorLogs.length > 100) serverErrorLogs.shift();
}

function getBinaryPath() {
  if (process.env.ELECTRON_BACKEND_PATH) {
    return path.resolve(process.env.ELECTRON_BACKEND_PATH);
  }
  const binaryName = process.platform === 'win32' ? 'new-api.exe' : 'new-api';
  if (process.env.NODE_ENV === 'development') {
    return path.join(__dirname, '..', binaryName);
  }
  return path.join(process.resourcesPath, 'bin', binaryName);
}

function saveErrorLog() {
  const logDir = app.getPath('logs');
  fs.mkdirSync(logDir, { recursive: true });
  const timestamp = new Date().toISOString().replaceAll(/[:.]/g, '-');
  const logPath = path.join(logDir, `new-api-crash-${timestamp}.log`);
  const content = [
    `New API desktop error report`,
    `Generated: ${new Date().toISOString()}`,
    `Platform: ${process.platform}/${process.arch}`,
    `Version: ${app.getVersion()}`,
    '',
    ...serverErrorLogs,
  ].join('\n');
  fs.writeFileSync(logPath, content, { encoding: 'utf8', mode: 0o600 });
  return logPath;
}

async function showStartupError(error) {
  rememberServerError(error instanceof Error ? error.stack || error.message : String(error));
  const result = await dialog.showMessageBox({
    type: 'error',
    title: 'New API failed to start',
    message: 'The local backend did not become ready.',
    detail: `${error.message || error}\n\nReadiness URL: ${READY_URL}`,
    buttons: ['Quit', 'Save diagnostic log'],
    defaultId: 0,
    cancelId: 0,
  });
  if (result.response === 1) {
    const logPath = saveErrorLog();
    const openError = await shell.openPath(logPath);
    if (openError) shell.showItemInFolder(logPath);
  }
}

async function startBackend() {
  if (process.env.NODE_ENV === 'development') {
    await waitForReady({
      url: `http://127.0.0.1:${DEV_FRONTEND_PORT}`,
      maxAttempts: 60,
      retryDelayMs: 500,
    });
    return;
  }

  const dataDir = path.join(app.getPath('userData'), 'data');
  fs.mkdirSync(dataDir, { recursive: true, mode: 0o700 });
  process.env.ELECTRON_DATA_DIR = dataDir;

  backendRuntime = createBackendRuntime({
    binaryPath: getBinaryPath(),
    cwd: process.resourcesPath,
    env: {
      ...process.env,
      PORT: String(PORT),
      SERVER_BIND_ADDRESS: '127.0.0.1',
      SQLITE_PATH: path.join(dataDir, 'new-api.db'),
    },
    readinessUrl: READY_URL,
    maxAttempts: Number(process.env.ELECTRON_READY_ATTEMPTS || 60),
    retryDelayMs: Number(process.env.ELECTRON_READY_DELAY_MS || 500),
    onStdout: (message) => console.log(`Backend: ${message.trimEnd()}`),
    onStderr: (message) => {
      rememberServerError(message);
      console.error(`Backend: ${message.trimEnd()}`);
    },
  });
  const child = await backendRuntime.start();
  backendReady = true;
  child.once('close', async (code, signal) => {
    backendReady = false;
    if (quitting) return;
    rememberServerError(`Backend exited unexpectedly (code=${code}, signal=${signal})`);
    await dialog.showMessageBox({
      type: 'error',
      title: 'New API backend stopped',
      message: 'The local backend exited unexpectedly.',
      detail: serverErrorLogs.slice(-20).join('\n'),
      buttons: ['Quit'],
    });
    app.quit();
  });
}

function createWindow() {
  const isDev = process.env.NODE_ENV === 'development';
  const port = isDev ? DEV_FRONTEND_PORT : PORT;
  mainWindow = new BrowserWindow({
    width: 1080,
    height: 720,
    icon: path.join(__dirname, 'icon.png'),
    title: 'New API',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
    },
  });

  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith('https://')) void shell.openExternal(url);
    return { action: 'deny' };
  });
  mainWindow.webContents.on('will-navigate', (event, url) => {
    const allowedOrigin = `http://127.0.0.1:${port}`;
    if (!isAllowedNavigation(url, allowedOrigin)) event.preventDefault();
  });
  void mainWindow.loadURL(`http://127.0.0.1:${port}`);
  if (isDev) mainWindow.webContents.openDevTools();

  mainWindow.on('close', (event) => {
    if (!quitting) {
      event.preventDefault();
      mainWindow.hide();
      if (process.platform === 'darwin') app.dock.hide();
    }
  });
  mainWindow.on('closed', () => {
    mainWindow = null;
  });
}

function showWindow() {
  if (!mainWindow) createWindow();
  mainWindow.show();
  if (process.platform === 'darwin') app.dock.show();
}

function createTray() {
  const icon =
    process.platform === 'darwin'
      ? 'tray-iconTemplate.png'
      : 'tray-icon-windows.png';
  tray = new Tray(path.join(__dirname, icon));
  tray.setToolTip('New API');
  tray.setContextMenu(
    Menu.buildFromTemplate([
      { label: 'Show New API', click: showWindow },
      { type: 'separator' },
      { label: 'Quit', click: () => app.quit() },
    ])
  );
  tray.on('click', showWindow);
}

async function shutdown() {
  if (quitting && !backendRuntime) return;
  quitting = true;
  backendReady = false;
  await backendRuntime?.stop({ graceMs: 5_000 });
  backendRuntime = null;
}

app.whenReady().then(async () => {
  try {
    await startBackend();
    createTray();
    createWindow();
  } catch (error) {
    await showStartupError(error);
    await shutdown();
    app.quit();
  }
});

app.on('activate', () => {
  if (BrowserWindow.getAllWindows().length === 0 && backendReady) showWindow();
});
app.on('window-all-closed', () => {});
app.on('before-quit', (event) => {
  if (!quitting && backendRuntime) {
    event.preventDefault();
    void shutdown().finally(() => app.exit(0));
  }
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, () => app.quit());
}
