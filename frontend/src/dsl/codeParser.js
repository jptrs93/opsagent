import {CODE_PUNCTUATION, SyntaxFailure, TokenStream, failureDiagnostic} from "./lexer.js";

class CodeParser {
    constructor(text) {
        this.text = text;
        this.stream = new TokenStream(text, CODE_PUNCTUATION);
    }

    parse() {
        const body = [];
        while (this.stream.current().type !== "eof") body.push(this.statement());
        return {kind: "document", body, from: 0, to: this.text.length};
    }

    statement() {
        const kind = this.stream.current().type;
        const name = kind === "string" || kind === "number"
            ? this.stream.take(kind)
            : this.stream.take("identifier", "Expected an attribute or block name.");
        if (kind !== "identifier") this.stream.take("=", `Expected '=' after the key ${JSON.stringify(name.value)}.`);
        if (kind !== "identifier" || this.stream.accept("=")) {
            const value = this.expression();
            return {kind: "attribute", name: name.value, nameToken: name, value, from: name.from, to: value.to};
        }
        if (this.stream.accept("{")) {
            const body = [];
            while (this.stream.current().type !== "}" && this.stream.current().type !== "eof") body.push(this.statement());
            const close = this.stream.take("}", `Block ${name.value} is not closed.`);
            return {kind: "block", name: name.value, nameToken: name, body, from: name.from, to: close.to};
        }
        this.stream.fail(`Expected '=' or '{' after ${name.value}. Block labels are not supported.`);
    }

    expression() {
        const token = this.stream.current();
        if (token.type === "string") {
            this.stream.index++;
            return {kind: "string", value: token.value, from: token.from, to: token.to};
        }
        if (token.type === "number") {
            this.stream.index++;
            const value = Number(token.value);
            if (!token.value.includes(".") && !Number.isSafeInteger(value)) this.stream.fail("Integer is outside the safe range.", token);
            return {kind: "number", value, from: token.from, to: token.to};
        }
        if (token.type === "identifier") {
            this.stream.index++;
            if ((token.value === "true" || token.value === "false") && this.stream.current().type !== "(") {
                return {kind: "boolean", value: token.value === "true", from: token.from, to: token.to};
            }
            this.stream.take("(", `Expected '(' after ${token.value}.`);
            return this.call(token);
        }
        if (this.stream.accept("[")) return this.list(token.from);
        if (this.stream.accept("{")) return this.object(token.from);
        this.stream.fail("Expected a string, integer, boolean, list, object, or function call.", token);
    }

    call(name) {
        const args = [];
        if (this.stream.current().type !== ")") {
            while (true) {
                args.push(this.expression());
                if (!this.stream.accept(",")) break;
                if (this.stream.current().type === ")") break;
            }
        }
        const close = this.stream.take(")", `Call to ${name.value} must separate arguments with commas and end with ')'.`);
        return {kind: "call", name: name.value, nameToken: name, args, from: name.from, to: close.to};
    }

    list(from) {
        const items = [];
        if (this.stream.current().type !== "]") {
            while (true) {
                items.push(this.expression());
                if (!this.stream.accept(",")) break;
                if (this.stream.current().type === "]") break;
            }
        }
        const close = this.stream.take("]", "List items must be separated with commas and the list must end with ']'.");
        return {kind: "list", items, from, to: close.to};
    }

    object(from) {
        const entries = [];
        while (this.stream.current().type !== "}" && this.stream.current().type !== "eof") {
            const kind = this.stream.current().type;
            const name = kind === "string" || kind === "number"
                ? this.stream.take(kind)
                : this.stream.take("identifier", "Expected a name, quoted key, or integer key in the object.");
            this.stream.take("=", `Expected '=' after ${name.value}.`);
            const value = this.expression();
            entries.push({kind: "attribute", name: name.value, nameToken: name, value, from: name.from, to: value.to});
            this.stream.accept(",");
        }
        const close = this.stream.take("}", "Object is not closed.");
        return {kind: "object", entries, from, to: close.to};
    }
}

export function parseCode(text) {
    const source = String(text ?? "");
    try {
        return {ast: new CodeParser(source).parse(), diagnostics: []};
    } catch (error) {
        if (error instanceof SyntaxFailure) return {ast: null, diagnostics: [failureDiagnostic(source, error)]};
        throw error;
    }
}
