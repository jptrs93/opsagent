const textDecoder = new TextDecoder();
const textEncoder = new TextEncoder();

export function registrationOptionsFromJSONBytes(bytes) {
    const options = JSON.parse(textDecoder.decode(bytes));
    const publicKey = options.publicKey ?? {};
    publicKey.challenge = base64UrlToBuffer(publicKey.challenge);
    if (publicKey.user?.id !== undefined) {
        publicKey.user.id = base64UrlToBuffer(publicKey.user.id);
    }
    publicKey.excludeCredentials = (publicKey.excludeCredentials ?? []).map((credential) => ({
        ...credential,
        id: base64UrlToBuffer(credential.id),
    }));
    return {...options, publicKey};
}

export function loginOptionsFromJSONBytes(bytes) {
    const options = JSON.parse(textDecoder.decode(bytes));
    const publicKey = options.publicKey ?? {};
    publicKey.challenge = base64UrlToBuffer(publicKey.challenge);
    publicKey.allowCredentials = (publicKey.allowCredentials ?? []).map((credential) => ({
        ...credential,
        id: base64UrlToBuffer(credential.id),
    }));
    return {...options, publicKey};
}

export function credentialToJSONBytes(credential) {
    return textEncoder.encode(JSON.stringify(publicKeyCredentialToJSON(credential)));
}

export function browserSupportsPasskeys() {
    return typeof window !== "undefined" && !!window.PublicKeyCredential && !!navigator.credentials;
}

function publicKeyCredentialToJSON(value) {
    if (value instanceof ArrayBuffer) {
        return bufferToBase64Url(value);
    }
    if (ArrayBuffer.isView(value)) {
        return bufferToBase64Url(value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength));
    }
    if (Array.isArray(value)) {
        return value.map(publicKeyCredentialToJSON);
    }
    if (value instanceof PublicKeyCredential) {
        const json = {
            id: value.id,
            rawId: bufferToBase64Url(value.rawId),
            type: value.type,
            response: publicKeyCredentialToJSON(value.response),
            clientExtensionResults: value.getClientExtensionResults(),
        };
        if (value.authenticatorAttachment) {
            json.authenticatorAttachment = value.authenticatorAttachment;
        }
        return json;
    }
    if (typeof AuthenticatorAttestationResponse !== "undefined" && value instanceof AuthenticatorAttestationResponse) {
        return {
            clientDataJSON: bufferToBase64Url(value.clientDataJSON),
            attestationObject: bufferToBase64Url(value.attestationObject),
            transports: typeof value.getTransports === "function" ? value.getTransports() : undefined,
            authenticatorData: value.authenticatorData ? bufferToBase64Url(value.authenticatorData) : undefined,
            publicKey: value.publicKey ? bufferToBase64Url(value.publicKey) : undefined,
            publicKeyAlgorithm: value.publicKeyAlgorithm ?? undefined,
        };
    }
    if (typeof AuthenticatorAssertionResponse !== "undefined" && value instanceof AuthenticatorAssertionResponse) {
        return {
            clientDataJSON: bufferToBase64Url(value.clientDataJSON),
            authenticatorData: bufferToBase64Url(value.authenticatorData),
            signature: bufferToBase64Url(value.signature),
            userHandle: value.userHandle ? bufferToBase64Url(value.userHandle) : undefined,
        };
    }
    if (value && typeof value === "object") {
        const json = {};
        for (const [key, child] of Object.entries(value)) {
            if (typeof child === "function" || child === undefined) {
                continue;
            }
            json[key] = publicKeyCredentialToJSON(child);
        }
        if (typeof value.getTransports === "function") {
            json.transports = value.getTransports();
        }
        return json;
    }
    return value;
}

function base64UrlToBuffer(value) {
    if (!value) {
        return new Uint8Array();
    }
    const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized + "=".repeat((4 - (normalized.length % 4)) % 4);
    const binary = atob(padded);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) {
        bytes[i] = binary.charCodeAt(i);
    }
    return bytes;
}

function bufferToBase64Url(buffer) {
    const bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
    let binary = "";
    for (const byte of bytes) {
        binary += String.fromCharCode(byte);
    }
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}
