import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import {parseEchoBody, requestHTTPSIngress} from '../helpers/httpsClient.js';

const WEB_HOST = 'web.ingress.opendeploy.test';
const INGRESS_READY_TIMEOUT = 180_000;

// Polls a request through the terminating proxy and returns the parsed
// key=value body once it answers 200.
async function fetchEchoFields(path, options = {}) {
  let fields;
  await expect.poll(async () => {
    try {
      const response = await requestHTTPSIngress(WEB_HOST, {path, ...options});
      if (response.status !== 200) return response.status;
      fields = parseEchoBody(response.body);
      return 200;
    } catch (error) {
      return String(error?.message || error);
    }
  }, {message: `expected 200 from ${WEB_HOST}${path}`, timeout: INGRESS_READY_TIMEOUT}).toBe(200);
  return fields;
}

export const httpsHeaderCases = [
  {
    id: 'https-request-headers-forwarded',
    title: 'verify request headers pass through',
    description: 'Confirms custom, multi-value, and case-insensitive request headers reach the backend and client-supplied X-Forwarded-For is replaced rather than trusted.',
    requires: ['https-root-route-verified'],
    async run() {
      const fields = await fetchEchoFields('/headers', {headers: {
        'x-custom-one': 'alpha',
        'x-multi': ['a', 'b'],
        'X-MiXeD-CaSe': 'preserved',
        'x-forwarded-for': '203.0.113.7',
      }});
      expect(fields['header:x-custom-one']).toBe('alpha');
      expect(fields['header:x-multi']).toBe('a|b');
      expect(fields['header:x-mixed-case']).toBe('preserved');
      expect(fields['header:x-forwarded-proto']).toBe('https');
      expect(fields['header:x-forwarded-host']).toBe(WEB_HOST);
      expect(fields.host).toBe(WEB_HOST);
      // The proxy must replace the inbound X-Forwarded-For with the observed
      // client address; forwarding the spoofed value would let callers forge
      // their origin to backends.
      expect(fields['header:x-forwarded-for']).toBeTruthy();
      expect(fields['header:x-forwarded-for']).not.toContain('203.0.113.7');
    },
  },
  {
    id: 'https-hop-by-hop-headers-stripped',
    title: 'verify hop-by-hop request headers are stripped',
    description: 'Confirms a header named in the Connection header does not reach the backend.',
    requires: ['https-root-route-verified'],
    async run() {
      const fields = await fetchEchoFields('/headers', {headers: {
        connection: 'x-hop-header',
        'x-hop-header': 'must-not-forward',
      }});
      expect(fields['header:x-hop-header']).toBeUndefined();
    },
  },
  {
    id: 'https-request-body-length-forwarded',
    title: 'verify request body and content-length pass through',
    description: 'Confirms a POST body arrives intact with its declared Content-Length.',
    requires: ['https-root-route-verified'],
    async run() {
      const fields = await fetchEchoFields('/headers', {
        method: 'POST',
        body: Buffer.alloc(100, 'a'),
        headers: {'content-length': '100'},
      });
      expect(fields.method).toBe('POST');
      expect(fields['content-length']).toBe('100');
    },
  },
  {
    id: 'https-response-headers-forwarded',
    title: 'verify response headers pass through',
    description: 'Confirms custom and multi-value response headers reach the client while hop-by-hop response headers do not.',
    requires: ['https-root-route-verified'],
    async run() {
      const path = '/setheaders?h=X-Resp-One:alpha&h=X-Multi:a&h=X-Multi:b&h=Keep-Alive:timeout=7';
      let response;
      await expect.poll(async () => {
        try {
          response = await requestHTTPSIngress(WEB_HOST, {path});
          return response.status;
        } catch (error) {
          return String(error?.message || error);
        }
      }, {message: `expected 200 from ${WEB_HOST}${path}`, timeout: INGRESS_READY_TIMEOUT}).toBe(200);
      // The body confirms the backend really set every header, so a missing
      // header below proves the proxy stripped it rather than it never existing.
      expect(parseEchoBody(response.body).set).toBe('X-Resp-One:alpha,X-Multi:a,X-Multi:b,Keep-Alive:timeout=7');
      expect(response.headers['x-resp-one']).toBe('alpha');
      expect(response.headers['x-multi']).toBe('a, b');
      expect(response.headers['keep-alive']).toBeUndefined();
    },
  },
];
