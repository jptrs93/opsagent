// Text forms of the API's IpAddress and IpPrefix messages:
//   IpAddress {value: {ipv4: {octets}} | {ipv6: {octets}}}
//   IpPrefix  {value: {ipv4: {address: {octets}, prefixLength}} | {ipv6: {...}}}
// Octets are Uint8Arrays of 4 or 16 bytes. Parsing returns null for text
// that is not an address (or prefix); formatting returns '' for a missing or
// malformed message.

const IPV4_RE = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

export function parseIPv4Octets(text) {
    const match = IPV4_RE.exec((text || '').trim());
    if (!match) return null;
    const octets = match.slice(1).map(Number);
    if (octets.some(octet => octet > 255)) return null;
    return Uint8Array.from(octets);
}

export function parseIPv6Octets(text) {
    let input = (text || '').trim();
    if (!input.includes(':') || /[^0-9a-fA-F:.]/.test(input)) return null;
    const words = [];
    const lastColon = input.lastIndexOf(':');
    if (input.includes('.')) {
        const v4 = parseIPv4Octets(input.slice(lastColon + 1));
        if (!v4) return null;
        input = `${input.slice(0, lastColon + 1)}${((v4[0] << 8) | v4[1]).toString(16)}:${((v4[2] << 8) | v4[3]).toString(16)}`;
    }
    const halves = input.split('::');
    if (halves.length > 2) return null;
    const parseWords = part => {
        if (part === '') return [];
        const items = part.split(':');
        const out = [];
        for (const item of items) {
            if (!/^[0-9a-fA-F]{1,4}$/.test(item)) return null;
            out.push(parseInt(item, 16));
        }
        return out;
    };
    const head = parseWords(halves[0]);
    const tail = halves.length === 2 ? parseWords(halves[1]) : [];
    if (!head || !tail) return null;
    if (halves.length === 2) {
        if (head.length + tail.length > 7) return null;
        words.push(...head, ...new Array(8 - head.length - tail.length).fill(0), ...tail);
    } else {
        if (head.length !== 8) return null;
        words.push(...head);
    }
    const octets = new Uint8Array(16);
    words.forEach((word, index) => {
        octets[index * 2] = word >> 8;
        octets[index * 2 + 1] = word & 0xff;
    });
    return octets;
}

export function formatIPv4Octets(octets) {
    if (!octets || octets.length !== 4) return '';
    return Array.from(octets).join('.');
}

// formatIPv6Octets renders the RFC 5952 text form: lower-case hex, the
// longest run of two or more zero words compressed to '::'.
export function formatIPv6Octets(octets) {
    if (!octets || octets.length !== 16) return '';
    const words = [];
    for (let index = 0; index < 16; index += 2) words.push((octets[index] << 8) | octets[index + 1]);
    let bestStart = -1;
    let bestLength = 0;
    for (let index = 0; index < 8;) {
        if (words[index] !== 0) {
            index++;
            continue;
        }
        let end = index;
        while (end < 8 && words[end] === 0) end++;
        if (end - index > bestLength) {
            bestStart = index;
            bestLength = end - index;
        }
        index = end;
    }
    const hex = words.map(word => word.toString(16));
    if (bestLength < 2) return hex.join(':');
    const head = hex.slice(0, bestStart).join(':');
    const tail = hex.slice(bestStart + bestLength).join(':');
    return `${head}::${tail}`;
}

export function parseIpAddress(text) {
    const v4 = parseIPv4Octets(text);
    if (v4) return {value: {ipv4: {octets: v4}}};
    const v6 = parseIPv6Octets(text);
    if (v6) return {value: {ipv6: {octets: v6}}};
    return null;
}

export function formatIpAddress(address) {
    const value = address?.value || {};
    if (value.ipv4) return formatIPv4Octets(value.ipv4.octets);
    if (value.ipv6) return formatIPv6Octets(value.ipv6.octets);
    return '';
}

// parseIpPrefix accepts "a.b.c.d", "a.b.c.d/n", "x::y", and "x::y/n". A bare
// address is the host prefix (/32 or /128).
export function parseIpPrefix(text) {
    const segments = (text || '').trim().split('/');
    if (segments.length > 2) return null;
    const [addressText, bitsText] = segments;
    const address = parseIpAddress(addressText);
    if (!address) return null;
    const family = address.value.ipv4 ? 'ipv4' : 'ipv6';
    const maxBits = family === 'ipv4' ? 32 : 128;
    let prefixLength = maxBits;
    if (bitsText !== undefined) {
        if (!/^\d{1,3}$/.test(bitsText) || Number(bitsText) > maxBits) return null;
        prefixLength = Number(bitsText);
    }
    return {value: {[family]: {address: address.value[family], prefixLength}}};
}

export function isIpPrefixText(text) {
    return parseIpPrefix(text) !== null;
}

export function ipPrefixLength(prefix) {
    const value = prefix?.value || {};
    const entry = value.ipv4 || value.ipv6;
    return entry ? Number(entry.prefixLength || 0) : 0;
}

export function ipPrefixAddress(prefix) {
    const value = prefix?.value || {};
    if (value.ipv4) return {value: {ipv4: value.ipv4.address}};
    if (value.ipv6) return {value: {ipv6: value.ipv6.address}};
    return null;
}

// formatIpPrefix omits the length of a host prefix, so a stored "/32" or
// "/128" reads back as the address that was typed.
export function formatIpPrefix(prefix) {
    const address = formatIpAddress(ipPrefixAddress(prefix));
    if (!address) return '';
    const maxBits = prefix.value.ipv4 ? 32 : 128;
    const length = ipPrefixLength(prefix);
    return length === maxBits ? address : `${address}/${length}`;
}

export const IPV4_ANY_PREFIX = '0.0.0.0/0';
export const IPV6_ANY_PREFIX = '::/0';

export function isIpPrefixMessage(value) {
    const inner = value?.value;
    return Boolean(inner && typeof inner === 'object' && (inner.ipv4?.address?.octets || inner.ipv6?.address?.octets));
}

export function isIpAddressMessage(value) {
    const inner = value?.value;
    return Boolean(inner && typeof inner === 'object' && (inner.ipv4?.octets || inner.ipv6?.octets));
}
