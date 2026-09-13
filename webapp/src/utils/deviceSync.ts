export const DEFAULT_SERVER_AUTHORITATIVE = true;

export const canImportDeviceSettings = (serverAuthoritative: boolean) =>
  !serverAuthoritative;

export const deviceSyncMessage = (
  serverAuthoritative: boolean,
  pending: boolean
) => {
  if (pending)
    return 'Latest server settings are pending the next successful device fetch.';
  if (serverAuthoritative)
    return 'The server is authoritative. Latest settings were pushed through the existing device transport.';
  return 'Device and server settings use legacy timestamp reconciliation.';
};
