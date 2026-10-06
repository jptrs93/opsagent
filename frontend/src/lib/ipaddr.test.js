import assert from "node:assert/strict";
import {test} from "node:test";
import {
    formatIpAddress,
    formatIpPrefix,
    isIpAddressMessage,
    isIpPrefixMessage,
    parseIpAddress,
    parseIpPrefix,
} from "./ipaddr.js";

const v4 = (...octets) => Uint8Array.from(octets);

test("parses and formats IPv4 addresses and prefixes", () => {
    assert.deepEqual(parseIpAddress("203.0.113.10"), {value: {ipv4: {octets: v4(203, 0, 113, 10)}}});
    assert.deepEqual(parseIpPrefix("198.51.100.0/24"), {value: {ipv4: {address: {octets: v4(198, 51, 100, 0)}, prefixLength: 24}}});
    assert.deepEqual(parseIpPrefix(" 203.0.113.10 "), {value: {ipv4: {address: {octets: v4(203, 0, 113, 10)}, prefixLength: 32}}});
    assert.equal(formatIpPrefix(parseIpPrefix("198.51.100.0/24")), "198.51.100.0/24");
    assert.equal(formatIpPrefix(parseIpPrefix("203.0.113.10")), "203.0.113.10");
    assert.equal(formatIpPrefix(parseIpPrefix("0.0.0.0/0")), "0.0.0.0/0");
    assert.equal(formatIpAddress(parseIpAddress("10.7.20.100")), "10.7.20.100");
});

test("parses and formats IPv6 in the RFC 5952 text form", () => {
    for (const [input, expected] of [
        ["2001:db8::10", "2001:db8::10"],
        ["2001:0DB8:0000:0000:0000:0000:0000:0010", "2001:db8::10"],
        ["::1", "::1"],
        ["::", "::"],
        ["fd00:0:0:1::", "fd00:0:0:1::"],
        ["2001:db8:0:0:1:0:0:1", "2001:db8::1:0:0:1"],
        ["::ffff:192.0.2.1", "::ffff:c000:201"],
        ["1:2:3:4:5:6:7:8", "1:2:3:4:5:6:7:8"],
    ]) {
        assert.equal(formatIpAddress(parseIpAddress(input)), expected, input);
    }
    assert.deepEqual(parseIpPrefix("2001:db8::/32").value.ipv6.prefixLength, 32);
    assert.equal(formatIpPrefix(parseIpPrefix("2001:db8::/32")), "2001:db8::/32");
    assert.equal(formatIpPrefix(parseIpPrefix("2001:db8::10")), "2001:db8::10");
    assert.equal(formatIpPrefix(parseIpPrefix("::/0")), "::/0");
});

test("rejects text that is not an address or prefix", () => {
    for (const bad of ["", "example.com", "256.0.0.1", "1.2.3", "1.2.3.4/33", "::/129", "1:::2", "1:2:3:4:5:6:7:8:9", "g::1", "1.2.3.4/x", "1.2.3.4/8/8"]) {
        assert.equal(parseIpPrefix(bad), null, bad);
        assert.equal(parseIpAddress(bad), null, bad);
    }
    assert.equal(formatIpPrefix(null), "");
    assert.equal(formatIpAddress({value: {}}), "");
});

test("tells prefix and address messages apart", () => {
    assert.equal(isIpPrefixMessage(parseIpPrefix("10.0.0.0/8")), true);
    assert.equal(isIpAddressMessage(parseIpPrefix("10.0.0.0/8")), false);
    assert.equal(isIpAddressMessage(parseIpAddress("10.0.0.1")), true);
    assert.equal(isIpPrefixMessage(parseIpAddress("10.0.0.1")), false);
    assert.equal(isIpPrefixMessage({value: 3}), false);
});
