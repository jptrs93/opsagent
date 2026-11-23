import {isFloatPrimitive, isTextPrimitive, strip} from "./schema.js";

const quote = value => JSON.stringify(String(value));

function align(entries) {
    const width = Math.max(...entries.map(([name]) => name.length));
    return entries.map(([name, text]) => `${name.padEnd(width)} = ${text}`);
}

export const raw = text => ({kind: "raw", text});
export const attribute = (name, value) => ({kind: "attribute", name, value});
export const block = (name, body) => ({kind: "block", name, body});

export function valueText(value) {
    switch (value.kind) {
        case "raw": return value.text;
        case "string": return quote(value.value);
        case "number": return String(value.value);
        case "boolean": return value.value ? "true" : "false";
        default: throw new Error(`Cannot print a ${value.kind} value.`);
    }
}

export function format(tree) {
    const lines = [];
    const indent = depth => "  ".repeat(depth);

    function statements(body, depth) {
        let previous = null;
        let run = [];
        const flush = () => {
            if (!run.length) return;
            const width = Math.max(...run.map(item => item.name.length));
            for (const item of run) {
                const [first, ...rest] = valueText(item.value).split("\n");
                lines.push(`${indent(depth)}${item.name.padEnd(width)} = ${first}`, ...rest);
            }
            run = [];
        };
        for (const statement of body) {
            if (statement.kind === "attribute") {
                if (previous === "block") lines.push("");
                run.push(statement);
                previous = "attribute";
                continue;
            }
            flush();
            if (previous) lines.push("");
            emitBlock(statement, depth);
            previous = "block";
        }
        flush();
    }

    function emitBlock(node, depth) {
        if (!node.body.length) {
            lines.push(`${indent(depth)}${node.name} {}`);
            return;
        }
        lines.push(`${indent(depth)}${node.name} {`);
        statements(node.body, depth + 1);
        lines.push(`${indent(depth)}}`);
    }

    emitBlock(tree, 0);
    return `${lines.join("\n")}\n`;
}

export function print(schema, rootName, instance, env = {}) {
    const root = schema.roots.get(rootName);
    if (!root) throw new Error(`${rootName} is not a root type.`);
    const printers = env.printers || {};
    const indent = depth => "  ".repeat(depth);

    const printerFor = type => {
        if (type.kind === "record") return printers[type.type.qualified] || printers[type.type.name];
        if (type.kind === "key") return printers[`${type.root.name}.id`];
        if (type.kind === "id") return printers[type.name];
        return undefined;
    };

    const helpers = depth => ({
        quote,
        render: (value, type) => {
            const text = expr(value, type, depth);
            if (text === null) throw new Error("Value cannot be printed inline.");
            return text;
        },
    });

    function isZero(value, type) {
        switch (type.kind) {
            case "optional": return value === undefined || value === null;
            case "prim": case "id": return isTextPrimitive(type.kind === "prim" ? type.name : type.prim) ? value === "" : value === false || value === 0;
            case "enum": return value === type.type.cases[0];
            case "list": case "array": return value.length === 0 || (type.kind === "array" && value.every(item => isZero(item, type.item)));
            case "map": return Object.keys(value).length === 0;
            case "record": return type.type.fields.every(field => {
                if (field.name === "id" && type.type.root) return true;
                const item = value[field.name];
                return item === undefined || item === null || (field.type.kind !== "optional" && isZero(item, field.type));
            });
            default: return false;
        }
    }

    const chosen = (value, union) => union.alternatives.find(alt => value[alt.codeName] !== undefined && value[alt.codeName] !== null);

    const carrier = recordType => {
        const fields = recordType.fields.filter(field => !(field.name === "id" && recordType.root));
        const carriers = fields.filter(field => field.type.kind !== "optional" && !["list", "array", "map"].includes(field.type.kind));
        return carriers.length === 1 ? carriers[0] : null;
    };

    function expr(value, type, depth) {
        const shape = strip(type);
        switch (shape.kind) {
            case "prim": case "id": {
                const prim = shape.kind === "prim" ? shape.name : shape.prim;
                if (prim === "bool") return value ? "true" : "false";
                if (isTextPrimitive(prim)) return quote(value);
                return isFloatPrimitive(prim) ? String(value) : String(Math.trunc(value));
            }
            case "enum": return quote(value);
            case "key": {
                const printer = printerFor(shape);
                if (!printer) throw new Error(`No printer for ${shape.root.name}.id references.`);
                return printer(value, helpers(depth));
            }
            case "list": case "array": {
                const items = value.map(item => expr(item, shape.item, depth + 1));
                if (items.some(item => item === null)) return null;
                const inline = items.join(", ");
                if (!items.length) return "[]";
                if (!inline.includes("\n") && inline.length <= 60) return `[${inline}]`;
                return `[\n${items.map(item => `${indent(depth + 1)}${item},`).join("\n")}\n${indent(depth)}]`;
            }
            case "map": {
                const keys = Object.keys(value);
                if (!keys.length) return "{}";
                const entries = [];
                for (const key of keys) {
                    const text = expr(value[key], shape.value, depth + 1);
                    if (text === null) return null;
                    entries.push([shape.key.name === "string" ? quote(key) : key, text]);
                }
                const inline = entries.map(([name, text]) => `${name} = ${text}`).join(", ");
                if (!inline.includes("\n") && inline.length <= 60) return `{ ${inline} }`;
                return `{\n${align(entries).map(line => `${indent(depth + 1)}${line}`).join("\n")}\n${indent(depth)}}`;
            }
            case "record": {
                const printer = printerFor(shape);
                if (printer) return printer(value, helpers(depth));
                const entries = [];
                for (const field of shape.type.fields) {
                    if (field.name === "id" && shape.type.root) continue;
                    const item = value[field.name];
                    if (item === undefined || item === null) continue;
                    if (field.type.kind !== "optional" && isZero(item, field.type)) continue;
                    const text = expr(item, field.type, depth + 1);
                    if (text === null) return null;
                    entries.push(`${field.name} = ${text}`);
                }
                if (!entries.length) return "{}";
                const inline = entries.join(", ");
                if (!inline.includes("\n") && inline.length <= 60) return `{ ${inline} }`;
                return `{\n${entries.map(entry => `${indent(depth + 1)}${entry}`).join("\n")}\n${indent(depth)}}`;
            }
            case "union": {
                const alt = chosen(value, shape);
                if (!alt) throw new Error("Union value has no alternative set.");
                const inner = value[alt.codeName];
                if (alt.type.kind === "record") {
                    if (printerFor(alt.type)) return printerFor(alt.type)(inner, helpers(depth));
                    const field = carrier(alt.type.type);
                    const othersZero = field && alt.type.type.fields.every(other => other === field
                        || other.name === "id" && alt.type.type.root
                        || inner[other.name] === undefined || inner[other.name] === null
                        || (other.type.kind !== "optional" && isZero(inner[other.name], other.type)));
                    if (field && othersZero && inner[field.name] !== undefined) return expr(inner[field.name], field.type, depth);
                    return null;
                }
                return expr(inner, alt.type, depth);
            }
            default: return null;
        }
    }

    function buildBlock(name, value, recordType, depth) {
        return block(name, body(value, recordType, depth + 1));
    }

    function buildUnionBlock(name, value, union, depth) {
        const alt = chosen(value, union);
        if (!alt || alt.type.kind !== "record") throw new Error(`${name} cannot be printed as a block.`);
        return block(name, [buildBlock(alt.codeName, value[alt.codeName], alt.type.type, depth + 1)]);
    }

    function unionInline(value, union, depth) {
        const alt = chosen(value, union);
        if (!alt) throw new Error("Union value has no alternative set.");
        const inner = value[alt.codeName];
        if (alt.type.kind !== "record") return expr(inner, alt.type, depth);
        if (printerFor(alt.type)) return printerFor(alt.type)(inner, helpers(depth));
        const field = carrier(alt.type.type);
        if (!field) return null;
        const shape = strip(field.type);
        const named = shape.kind === "key" || shape.kind === "id" || (shape.kind === "record" && printerFor(shape));
        if (!named) return null;
        const othersZero = alt.type.type.fields.every(other => other === field
            || (other.name === "id" && alt.type.type.root)
            || inner[other.name] === undefined || inner[other.name] === null
            || (other.type.kind !== "optional" && isZero(inner[other.name], other.type)));
        return othersZero && inner[field.name] !== undefined ? expr(inner[field.name], field.type, depth) : null;
    }

    const scalar = (value, type) => {
        const shape = strip(type);
        if (shape.kind === "prim" || shape.kind === "id") {
            const prim = shape.kind === "prim" ? shape.name : shape.prim;
            if (prim === "bool") return {kind: "boolean", value};
            if (isTextPrimitive(prim)) return {kind: "string", value};
            return {kind: "number", value: isFloatPrimitive(prim) ? value : Math.trunc(value)};
        }
        if (shape.kind === "enum") return {kind: "string", value};
        return null;
    };

    function body(value, type, depth) {
        const out = [];
        const attr = (name, text, node = null) => out.push(attribute(name, node || raw(text)));
        for (const field of type.fields) {
            if (field.name === "id" && type.root) continue;
            const item = value[field.name];
            if (item === undefined || item === null) continue;
            const shape = strip(field.type);
            if (field.type.kind !== "optional" && isZero(item, shape)) continue;
            if (shape.kind === "record") {
                if (printerFor(shape)) attr(field.name, expr(item, shape, depth));
                else out.push(buildBlock(field.name, item, shape.type, depth));
                continue;
            }
            if (shape.kind === "union") {
                const inline = unionInline(item, shape, depth);
                if (inline !== null) attr(field.name, inline);
                else out.push(buildUnionBlock(field.name, item, shape, depth));
                continue;
            }
            if (shape.kind === "list" || shape.kind === "array") {
                const itemShape = strip(shape.item);
                if (itemShape.kind === "record" && !printerFor(itemShape)) {
                    for (const entry of item) out.push(buildBlock(field.name, entry, itemShape.type, depth));
                    continue;
                }
                if (itemShape.kind === "union") {
                    const inlines = item.map(entry => unionInline(entry, itemShape, depth + 1));
                    if (inlines.some(text => text === null)) {
                        for (const entry of item) out.push(buildUnionBlock(field.name, entry, itemShape, depth));
                        continue;
                    }
                }
                attr(field.name, expr(item, shape, depth));
                continue;
            }
            attr(field.name, null, scalar(item, shape) || raw(expr(item, shape, depth)));
        }
        return out;
    }

    let tree = buildBlock(root.codeName, instance, root, 0);
    if (env.rewrite) tree = env.rewrite(tree) || tree;
    return format(tree);
}
