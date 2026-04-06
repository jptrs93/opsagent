import { Capi } from './capi.js';
import { handleErr } from './err.js';
import { loginS } from '../state/login.js';

const authHeaders = () => {
  const token = loginS.val?.token;
  return token ? { Authorization: `Bearer ${token}` } : {};
};

export const capi = new Capi('', authHeaders, handleErr);

export { Capi } from './capi.js';
