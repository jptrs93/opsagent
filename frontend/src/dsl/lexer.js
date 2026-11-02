export class SyntaxFailure extends Error {
    constructor(from, to, message) {
        super(message);
        this.from = from;
        this.to = to;
    }
}

export const CODE_PUNCTUATION = ["{", "}", "[", "]", "(", ")", ",", "="];
export const TYPE_PUNCTUATION = ["->", "{", "}", "(", ")", "[", "]", "<", ">", "?", "|", ".", ":", ",", "=", ";", "@"];

const isIdentifierStart = char => /[A-Za-z_]/.test(char);
const isIdentifierPart = char => /[A-Za-z0-9_-]/.test(char);
const isDigit = char => /[0-9]/.test(char);

export function tokenize(text, punctuation) {
    const tokens = [];
    let index = 0;
    const fail = (from, to, message) => { throw new SyntaxFailure(from, to, message); };

    while (index < text.length) {
        const char = text[index];
        if (char === " " || char === "\t" || char === "\n" || char === "\r") {
            index++;
            continue;
        }
        if (char === "#" || (char === "/" && text[index + 1] === "/")) {
            const newline = text.indexOf("\n", index + 1);
            index = newline < 0 ? text.length : newline + 1;
            continue;
        }
        if (char === "/" && text[index + 1] === "*") {
            const end = text.indexOf("*/", index + 2);
            if (end < 0) fail(index, text.length, "Unclosed block comment.");
            index = end + 2;
            continue;
        }
        if (char === '"') {
            const from = index++;
            let closed = false;
            while (index < text.length) {
                if (text[index] === "\n" || text[index] === "\r") fail(from, index, "Quoted strings cannot contain a literal newline.");
                if (text[index] === "\\") {
                    index += 2;
                    continue;
                }
                if (text[index] === '"') {
                    index++;
                    closed = true;
                    break;
                }
                index++;
            }
            if (!closed) fail(from, text.length, "Unclosed quoted string.");
            let value;
            try {
                value = JSON.parse(text.slice(from, index));
            } catch {
                fail(from, index, "String must use valid JSON escaping.");
            }
            tokens.push({type: "string", value, from, to: index});
            continue;
        }
        if (isIdentifierStart(char)) {
            const from = index++;
            while (index < text.length && isIdentifierPart(text[index])) index++;
            tokens.push({type: "identifier", value: text.slice(from, index), from, to: index});
            continue;
        }
        if (isDigit(char) || (char === "-" && isDigit(text[index + 1] || ""))) {
            const from = index++;
            while (index < text.length && isDigit(text[index])) index++;
            if (text[index] === "." && isDigit(text[index + 1] || "")) {
                index++;
                while (index < text.length && isDigit(text[index])) index++;
            }
            tokens.push({type: "number", value: text.slice(from, index), from, to: index});
            continue;
        }
        const symbol = punctuation.find(candidate => text.startsWith(candidate, index));
        if (symbol) {
            tokens.push({type: symbol, value: symbol, from: index, to: index + symbol.length});
            index += symbol.length;
            continue;
        }
        fail(index, index + 1, `Unexpected character ${JSON.stringify(char)}.`);
    }
    tokens.push({type: "eof", value: "", from: text.length, to: text.length});
    return tokens;
}

export class TokenStream {
    constructor(text, punctuation) {
        this.text = text;
        this.tokens = tokenize(text, punctuation);
        this.index = 0;
    }

    current() {
        return this.tokens[this.index];
    }

    peek(offset = 1) {
        return this.tokens[Math.min(this.index + offset, this.tokens.length - 1)];
    }

    take(type, message) {
        const token = this.current();
        if (token.type !== type) throw new SyntaxFailure(token.from, token.to, message || `Expected ${describeToken(type)}.`);
        this.index++;
        return token;
    }

    accept(type) {
        if (this.current().type !== type) return null;
        return this.tokens[this.index++];
    }

    fail(message, token = this.current()) {
        throw new SyntaxFailure(token.from, token.to, message);
    }
}

function describeToken(type) {
    return type === "identifier" || type === "string" || type === "number" || type === "eof" ? `a ${type === "eof" ? "end of input" : type}` : `'${type}'`;
}

export function failureDiagnostic(text, failure) {
    const length = text.length;
    const from = Math.max(0, Math.min(length, failure.from));
    const to = Math.max(from, Math.min(length, Math.max(failure.to, from + 1)));
    return {from, to, severity: "error", message: failure.message};
}
