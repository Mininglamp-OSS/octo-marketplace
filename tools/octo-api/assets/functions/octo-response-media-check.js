module.exports = (operation) => {
  if (!operation || typeof operation !== 'object') return;

  const byteStreams = {
    'plugin.skill_download': 'application/zip',
    'admin_plugin.download': 'application/zip',
    'plugin.version.download': 'application/octet-stream',
    'admin_plugin.version.download': 'application/octet-stream',
  };
  const allowedSuccess = byteStreams[operation.operationId] || 'application/json';
  const errors = [];

  for (const [status, response] of Object.entries(operation.responses || {})) {
    const mediaTypes = Object.keys((response && response.content) || {});
    const expected = /^[45]/.test(status) ? 'application/json' : (/^2/.test(status) ? allowedSuccess : null);
    if (!expected) continue;
    for (const mediaType of mediaTypes) {
      if (mediaType !== expected) {
        errors.push({message: `${status} response content must be ${expected}`});
      }
    }
  }
  return errors.length ? errors : undefined;
};
