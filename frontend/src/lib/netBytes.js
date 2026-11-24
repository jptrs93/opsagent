const counters = new Map();

let installed = false;

const pathOf = (input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input?.url;
    if (!url) return null;
    try {
        return new URL(url, globalThis.location?.href || "http://localhost/").pathname;
    } catch {
        return null;
    }
};

const counting = (body, path) => body.pipeThrough(new TransformStream({
    transform(chunk, controller) {
        counters.set(path, counters.get(path) + chunk.byteLength);
        controller.enqueue(chunk);
    },
}));

const install = () => {
    if (installed || typeof globalThis.fetch !== "function" || typeof TransformStream !== "function") return;
    installed = true;
    const nativeFetch = globalThis.fetch.bind(globalThis);
    globalThis.fetch = async (input, init) => {
        const response = await nativeFetch(input, init);
        const path = pathOf(input);
        if (path === null || !counters.has(path) || !response.body) return response;
        return new Response(counting(response.body, path), response);
    };
};

export function bytesReceivedOn(path) {
    if (!counters.has(path)) counters.set(path, 0);
    install();
    return () => counters.get(path) ?? 0;
}
