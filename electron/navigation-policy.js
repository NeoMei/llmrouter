function isAllowedNavigation(target, allowedOrigin) {
  try {
    const targetUrl = new URL(target);
    const allowedUrl = new URL(allowedOrigin);
    return (
      targetUrl.origin === allowedUrl.origin &&
      targetUrl.username === '' &&
      targetUrl.password === ''
    );
  } catch {
    return false;
  }
}

module.exports = { isAllowedNavigation };
