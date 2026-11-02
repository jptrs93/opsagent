import {SyntaxFailure, TokenStream, TYPE_PUNCTUATION, failureDiagnostic} from "./lexer.js";

export const PRIMITIVES = new Set(["bool", "string", "bytes", "uuid", "i32", "i64", "u8", "u32", "u64", "f32", "f64"]);
export const MAP_KEY_PRIMITIVES = new Set(["string", "bool", "i32", "i64", "u8", "u32", "u64"]);

const NOT_HERE = {
    laws: "Laws are not part of this language.",
    law: "Laws are not part of this language.",
    reserve: "Reserved tags are not part of this language.",
    transitions: "Enum transitions are not part of this language.",
    import: "Modules and imports are not supported; a definition is one file.",
    module: "Modules and imports are not supported; a definition is one file.",
};

export class TypeParser {
    constructor(text) {
        this.text = text;
        this.stream = new TokenStream(text, TYPE_PUNCTUATION);
    }

    parse() {
        const declarations = [];
        while (this.stream.current().type !== "eof") declarations.push(this.declaration());
        return declarations;
    }

    declaration() {
        const keyword = this.stream.take("identifier", "Expected a declaration: type, enum, or id.");
        switch (keyword.value) {
            case "type": return this.record(keyword, false);
            case "enum": return this.enumeration(keyword);
            case "id": return this.idDeclaration(keyword);
            default:
                if (NOT_HERE[keyword.value]) this.stream.fail(NOT_HERE[keyword.value], keyword);
                this.stream.fail(`Unknown declaration ${keyword.value}; expected type, enum, or id.`, keyword);
        }
    }

    typeName() {
        const token = this.stream.take("identifier", "Expected a type name.");
        if (PRIMITIVES.has(token.value)) this.stream.fail(`${token.value} is a primitive and cannot be a type name.`, token);
        return token;
    }

    noTag() {
        if (this.stream.current().type === "@") this.stream.fail("Tags are not part of this language; leave out @n.");
    }

    noMetadata() {
        if (this.stream.current().type === "[") this.stream.fail("Field constraints and metadata are not part of this language.");
    }

    record(keyword, inline) {
        const name = this.typeName();
        this.noTag();
        let root = false;
        let mat = false;
        while (this.stream.current().type === "identifier" && (this.stream.current().value === "root" || this.stream.current().value === "mat")) {
            const marker = this.stream.current();
            if (inline) this.stream.fail("An inline type cannot be a root or a mat.", marker);
            if (root || mat) this.stream.fail("A type is a root or a mat, not both.", marker);
            if (marker.value === "root") root = true;
            else mat = true;
            this.stream.index++;
        }
        this.stream.take("{", `Expected '{' to open type ${name.value}.`);
        const fields = [];
        while (this.stream.current().type !== "}" && this.stream.current().type !== "eof") fields.push(this.field());
        const close = this.stream.take("}", `type ${name.value} is not closed.`);
        return {kind: "record", name: name.value, nameToken: name, root, mat, fields, from: keyword.from, to: close.to};
    }

    field() {
        const name = this.stream.take("identifier", "Expected a field name.");
        if (NOT_HERE[name.value] && (this.stream.current().type === "{" || this.stream.current().type === "@" || name.value === "reserve")) {
            this.stream.fail(NOT_HERE[name.value], name);
        }
        this.noTag();
        this.stream.take(":", `Expected ':' after field ${name.value}.`);
        const type = this.typeExpr();
        this.noMetadata();
        if (!this.stream.accept(";")) this.stream.accept(",");
        return {name: name.value, nameToken: name, type, from: name.from, to: type.to};
    }

    enumeration(keyword) {
        const name = this.typeName();
        this.noTag();
        this.stream.take("{", `Expected '{' to open enum ${name.value}.`);
        const cases = [];
        while (this.stream.current().type !== "}" && this.stream.current().type !== "eof") {
            const token = this.stream.take("identifier", "Expected an enum case.");
            if (NOT_HERE[token.value] && (this.stream.current().type === "{" || token.value === "reserve")) this.stream.fail(NOT_HERE[token.value], token);
            this.noTag();
            cases.push({name: token.value, from: token.from, to: token.to});
            if (!this.stream.accept(";")) this.stream.accept(",");
        }
        const close = this.stream.take("}", `enum ${name.value} is not closed.`);
        return {kind: "enum", name: name.value, nameToken: name, cases, from: keyword.from, to: close.to};
    }

    idDeclaration(keyword) {
        const name = this.typeName();
        this.stream.take("=", `Expected '=' after id ${name.value}.`);
        const primitive = this.stream.take("identifier", "Expected a primitive after '='.");
        if (!PRIMITIVES.has(primitive.value)) this.stream.fail(`${primitive.value} is not a primitive.`, primitive);
        this.stream.accept(";");
        return {kind: "id", name: name.value, nameToken: name, primitive: primitive.value, from: keyword.from, to: primitive.to};
    }

    typeExpr() {
        const first = this.unaryType();
        if (this.stream.current().type !== "|") return first;
        const alternatives = [first];
        while (this.stream.accept("|")) alternatives.push(this.unaryType());
        return {kind: "union", alternatives, from: first.from, to: alternatives[alternatives.length - 1].to};
    }

    unaryType() {
        const token = this.stream.current();
        if (this.stream.accept("?")) {
            const item = this.unaryType();
            return {kind: "optional", item, from: token.from, to: item.to};
        }
        if (this.stream.accept("[")) {
            let length;
            const number = this.stream.accept("number");
            if (number) {
                length = Number(number.value);
                if (!Number.isInteger(length) || length < 0) this.stream.fail("Array length must be a non-negative integer.", number);
            }
            this.stream.take("]", "Expected ']' after '['.");
            const item = this.unaryType();
            return number
                ? {kind: "array", length, item, from: token.from, to: item.to}
                : {kind: "list", item, from: token.from, to: item.to};
        }
        if (this.stream.accept("(")) {
            const inner = this.typeExpr();
            const close = this.stream.take(")", "Expected ')' to close the grouped type.");
            this.noTag();
            return {...inner, from: token.from, to: close.to};
        }
        const ident = this.stream.take("identifier", "Expected a type.");
        if (ident.value === "type") {
            const declaration = this.record(ident, true);
            this.noTag();
            return {kind: "inline", declaration, from: ident.from, to: declaration.to};
        }
        if (ident.value === "enum") {
            const declaration = this.enumeration(ident);
            this.noTag();
            return {kind: "inline", declaration, from: ident.from, to: declaration.to};
        }
        if (ident.value === "map" && this.stream.current().type === "<") {
            this.stream.take("<");
            const key = this.typeExpr();
            this.stream.take(",", "Expected ',' between the map key and value types.");
            const value = this.typeExpr();
            const close = this.stream.take(">", "Expected '>' to close map<...>.");
            return {kind: "map", key, value, from: ident.from, to: close.to};
        }
        if (PRIMITIVES.has(ident.value)) {
            this.noTag();
            return {kind: "prim", name: ident.value, from: ident.from, to: ident.to};
        }
        const path = [ident.value];
        let to = ident.to;
        while (this.stream.accept(".")) {
            const segment = this.stream.take("identifier", "Expected a name after '.'.");
            path.push(segment.value);
            to = segment.to;
        }
        this.noTag();
        return {kind: "name", path, from: ident.from, to};
    }
}

export function parseTypeDefinitions(text) {
    const source = String(text ?? "");
    try {
        return {declarations: new TypeParser(source).parse(), diagnostics: []};
    } catch (error) {
        if (error instanceof SyntaxFailure) return {declarations: null, diagnostics: [failureDiagnostic(source, error)]};
        throw error;
    }
}

export function parseSignature(text) {
    const parser = new TypeParser(String(text ?? ""));
    const stream = parser.stream;
    stream.take("(", "A signature starts with '('.");
    const params = [];
    while (stream.current().type !== ")" && stream.current().type !== "eof") {
        const name = stream.take("identifier", "Expected a parameter name.");
        stream.take(":", `Expected ':' after parameter ${name.value}.`);
        const type = parser.typeExpr();
        params.push({name: name.value, type, optional: type.kind === "optional"});
        if (!stream.accept(",")) break;
    }
    stream.take(")", "Parameters must end with ')'.");
    stream.take("->", "Expected '->' and a return type.");
    const returns = parser.typeExpr();
    stream.take("eof", "Unexpected text after the return type.");
    return {params, returns};
}
