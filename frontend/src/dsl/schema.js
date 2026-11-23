import {MAP_KEY_PRIMITIVES, parseTypeDefinitions} from "./typeParser.js";

const at = (node, message) => ({from: node?.from ?? 0, to: node?.to ?? (node?.from ?? 0), severity: "error", message});

export function snakeCase(name) {
    return name
        .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
        .replace(/([A-Z]{2,})([A-Z][a-z])/g, "$1_$2")
        .toLowerCase();
}

export const INTEGER_RANGES = {
    u8: [0, 255],
    u32: [0, 4294967295],
    u64: [0, Number.MAX_SAFE_INTEGER],
    i32: [-2147483648, 2147483647],
    i64: [Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER],
};

export const isIntegerPrimitive = name => name in INTEGER_RANGES;
export const isTextPrimitive = name => name === "string" || name === "bytes" || name === "uuid";
export const isFloatPrimitive = name => name === "f32" || name === "f64";

export const strip = type => type.kind === "optional" ? type.item : type;

export function typeEquals(a, b) {
    if (!a || !b || a.kind !== b.kind) return false;
    switch (a.kind) {
        case "prim": return a.name === b.name;
        case "record": case "enum": return a.type === b.type;
        case "key": return a.root === b.root;
        case "id": return a.name === b.name;
        case "optional": case "list": return typeEquals(a.item, b.item);
        case "array": return a.length === b.length && typeEquals(a.item, b.item);
        case "map": return typeEquals(a.key, b.key) && typeEquals(a.value, b.value);
        case "union": return a.alternatives.length === b.alternatives.length
            && a.alternatives.every(alt => b.alternatives.some(other => typeEquals(alt.type, other.type)));
        default: return false;
    }
}

export function typeText(type) {
    switch (type.kind) {
        case "prim": return type.name;
        case "record": case "enum": return type.type.name;
        case "key": return `${type.root.name}.id`;
        case "id": return type.name;
        case "optional": return `?${typeText(type.item)}`;
        case "list": return `[]${typeText(type.item)}`;
        case "array": return `[${type.length}]${typeText(type.item)}`;
        case "map": return `map<${typeText(type.key)}, ${typeText(type.value)}>`;
        case "union": return type.alternatives.map(alt => typeText(alt.type)).join(" | ");
        default: return type.kind;
    }
}

export function describeType(type) {
    switch (type.kind) {
        case "prim":
            if (type.name === "bool") return "true or false";
            if (isIntegerPrimitive(type.name)) return "an integer";
            if (isFloatPrimitive(type.name)) return "a number";
            return "a string";
        case "enum": return `one of ${type.type.cases.map(name => JSON.stringify(name)).join(", ")}`;
        case "key": return `a ${type.root.name} reference`;
        case "id": return `a ${type.name} value`;
        case "record": return `a ${type.type.name} block`;
        case "optional": return describeType(type.item);
        case "list": return `a list of ${describeType(type.item).replace(/^(?:an? |one of )/, "")} values`;
        case "array": return `a list of exactly ${type.length} ${describeType(type.item).replace(/^(?:an? |one of )/, "")} values`;
        case "map": return "an object of named values";
        case "union": return `one of ${type.alternatives.map(alt => typeText(alt.type)).join(", ")}`;
        default: return type.kind;
    }
}

export function linkSchema(declarations) {
    const diagnostics = [];
    const report = (node, message) => { diagnostics.push(at(node, message)); };
    const schema = {types: new Map(), roots: new Map(), top: new Map(), ids: new Map(), declarations};

    function register(declaration, parent) {
        const qualified = parent ? `${parent.qualified}.${declaration.name}` : declaration.name;
        const scope = parent ? parent.nested : schema.top;
        if (scope.has(declaration.name) || (!parent && schema.ids.has(declaration.name))) {
            report(declaration.nameToken, `${declaration.name} is declared more than once${parent ? ` in ${parent.qualified}` : ""}.`);
            return null;
        }
        const type = declaration.kind === "record"
            ? {kind: "record", name: declaration.name, qualified, codeName: snakeCase(declaration.name), root: declaration.root, mat: declaration.mat, fields: [], fieldMap: new Map(), nested: new Map(), parent, node: declaration}
            : {kind: "enum", name: declaration.name, qualified, codeName: snakeCase(declaration.name), cases: [], parent, node: declaration};
        scope.set(declaration.name, type);
        schema.types.set(qualified, type);
        if (type.root) schema.roots.set(declaration.name, type);
        if (declaration.kind === "enum") {
            for (const item of declaration.cases) {
                if (type.cases.includes(item.name)) report(item, `Enum case ${item.name} is repeated in ${qualified}.`);
                else type.cases.push(item.name);
            }
            if (!type.cases.length) report(declaration.nameToken, `enum ${qualified} has no cases.`);
            return type;
        }
        for (const field of declaration.fields) registerInline(field.type, type);
        return type;
    }

    function registerInline(expr, owner) {
        switch (expr.kind) {
            case "inline": register(expr.declaration, owner); break;
            case "optional": case "list": case "array": registerInline(expr.item, owner); break;
            case "map": registerInline(expr.key, owner); registerInline(expr.value, owner); break;
            case "union": for (const alt of expr.alternatives) registerInline(alt, owner); break;
            default: break;
        }
    }

    for (const declaration of declarations) {
        if (declaration.kind === "id") {
            if (schema.ids.has(declaration.name) || schema.top.has(declaration.name)) report(declaration.nameToken, `${declaration.name} is declared more than once.`);
            else schema.ids.set(declaration.name, declaration.primitive);
            continue;
        }
        register(declaration, null);
    }

    const lookup = (name, scope) => {
        for (let current = scope; current; current = current.parent) {
            if (current.nested?.has(name)) return current.nested.get(name);
        }
        return schema.top.get(name) || null;
    };

    const typeRef = type => type.kind === "record" ? {kind: "record", type} : {kind: "enum", type};

    function resolve(expr, scope) {
        switch (expr.kind) {
            case "prim": return {kind: "prim", name: expr.name};
            case "optional": {
                const item = resolve(expr.item, scope);
                if (!item) return null;
                if (item.kind === "optional") {
                    report(expr, "An optional of an optional is not allowed.");
                    return null;
                }
                return {kind: "optional", item};
            }
            case "list": case "array": {
                const item = resolve(expr.item, scope);
                if (!item) return null;
                return expr.kind === "list" ? {kind: "list", item} : {kind: "array", length: expr.length, item};
            }
            case "map": {
                const key = resolve(expr.key, scope);
                const value = resolve(expr.value, scope);
                if (!key || !value) return null;
                if (key.kind !== "prim" || !MAP_KEY_PRIMITIVES.has(key.name)) {
                    report(expr.key, `Map keys must be one of ${[...MAP_KEY_PRIMITIVES].join(", ")}.`);
                    return null;
                }
                return {kind: "map", key, value};
            }
            case "inline": {
                const type = scope ? scope.nested.get(expr.declaration.name) : schema.top.get(expr.declaration.name);
                return type ? typeRef(type) : null;
            }
            case "name": {
                const [first, ...rest] = expr.path;
                if (!lookup(first, scope) && schema.ids.has(first)) {
                    if (rest.length) {
                        report(expr, `${first} is an id and has no members.`);
                        return null;
                    }
                    return {kind: "id", name: first, prim: schema.ids.get(first)};
                }
                let current = lookup(first, scope);
                if (!current && !scope) {
                    const named = [...schema.types.values()].filter(type => type.name === first);
                    if (named.length > 1) {
                        report(expr, `${first} names ${named.length} nested types; qualify it, for example ${named[0].qualified}.`);
                        return null;
                    }
                    current = named[0] || null;
                }
                if (!current) {
                    report(expr, `Unknown type ${expr.path.join(".")}.`);
                    return null;
                }
                for (const [index, segment] of rest.entries()) {
                    if (current.kind === "record" && current.nested.has(segment)) {
                        current = current.nested.get(segment);
                        continue;
                    }
                    if (current.kind === "record" && segment === "id" && index === rest.length - 1) {
                        const idField = current.node.fields.find(field => field.name === "id");
                        if (!current.root) {
                            report(expr, `${current.qualified} is not a root, so ${expr.path.join(".")} cannot be a key reference.`);
                            return null;
                        }
                        if (!idField || idField.type.kind !== "prim") {
                            report(expr, `${current.qualified} has no primitive id field to reference.`);
                            return null;
                        }
                        return {kind: "key", root: current, prim: idField.type.name};
                    }
                    report(expr, `${current.qualified} has no nested type or key named ${segment}.`);
                    return null;
                }
                return typeRef(current);
            }
            case "union": {
                const alternatives = [];
                for (const alt of expr.alternatives) {
                    const resolved = resolve(alt, scope);
                    if (!resolved) continue;
                    if (!["record", "enum", "prim", "key", "id"].includes(resolved.kind)) {
                        report(alt, "Union alternatives must be types, enums, primitives, or key references.");
                        continue;
                    }
                    const codeName = resolved.kind === "record" || resolved.kind === "enum" ? resolved.type.codeName
                        : resolved.kind === "key" ? `${resolved.root.codeName}_id`
                            : resolved.name;
                    if (alternatives.some(other => other.codeName === codeName)) report(alt, `Union alternative ${codeName} is repeated.`);
                    else if (alternatives.some(other => typeEquals(other.type, resolved))) report(alt, `Union alternative ${typeText(resolved)} is repeated.`);
                    else alternatives.push({type: resolved, codeName});
                }
                if (alternatives.length < 2 && !diagnostics.length) report(expr, "A union needs at least two alternatives.");
                return alternatives.length ? {kind: "union", alternatives} : null;
            }
            default:
                report(expr, `Unsupported type expression ${expr.kind}.`);
                return null;
        }
    }

    for (const type of schema.types.values()) {
        if (type.kind !== "record") continue;
        for (const field of type.node.fields) {
            if (type.fieldMap.has(field.name)) {
                report(field.nameToken, `Field ${field.name} is declared more than once in ${type.qualified}.`);
                continue;
            }
            const resolved = resolve(field.type, type);
            if (!resolved) continue;
            const linked = {name: field.name, type: resolved, node: field};
            type.fields.push(linked);
            type.fieldMap.set(field.name, linked);
        }
    }

    schema.resolve = expr => resolve(expr, null);
    return {schema: diagnostics.length ? null : schema, diagnostics};
}

export function compileSchema(text) {
    const parsed = parseTypeDefinitions(text);
    if (!parsed.declarations) return {schema: null, diagnostics: parsed.diagnostics};
    return linkSchema(parsed.declarations);
}
