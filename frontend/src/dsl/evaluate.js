import {parseCode} from "./codeParser.js";
import {callHint} from "./functions.js";
import {INTEGER_RANGES, describeType, isFloatPrimitive, isIntegerPrimitive, isTextPrimitive, strip, typeEquals, typeText} from "./schema.js";

const at = (node, message, severity = "error") => ({from: node?.from ?? 0, to: node?.to ?? (node?.from ?? 0), severity, message});

const isCollection = type => type.kind === "list" || type.kind === "array" || type.kind === "map";

export class Evaluator {
    constructor(schema, functions, diagnostics) {
        this.schema = schema;
        this.functions = functions;
        this.diagnostics = diagnostics;
        this.nodes = new WeakMap();
        this.root = null;
    }

    report(node, message) {
        this.diagnostics.push(at(node, message));
        return null;
    }

    remember(value, node) {
        if (value && typeof value === "object") this.nodes.set(value, node);
        return value;
    }

    document(ast, rootType) {
        const roots = [];
        for (const statement of ast.body) {
            if (statement.kind === "block" && statement.name === rootType.codeName) roots.push(statement);
            else this.report(statement.nameToken, `Only a ${rootType.codeName} block may appear at the top level.`);
        }
        if (roots.length !== 1) this.report(roots[1] || ast, `Document requires exactly one ${rootType.codeName} block.`);
        if (!roots.length) return null;
        this.root = this.remember({}, roots[0]);
        return this.record(roots[0].body, rootType, roots[0], this.root);
    }

    blockable(type) {
        switch (type.kind) {
            case "record": case "map": return true;
            case "union": return type.alternatives.some(alt => alt.type.kind === "record");
            case "list": case "array": return type.item.kind === "record" || (type.item.kind === "union" && this.blockable(type.item));
            default: return false;
        }
    }

    record(statements, type, owner, into = this.remember({}, owner)) {
        const grouped = new Map();
        for (const statement of statements) {
            const field = type.fieldMap.get(statement.name);
            const asBlock = statement.kind === "block";
            if (!field) {
                this.report(statement.nameToken, `${type.name} has no field named ${statement.name}.`);
                continue;
            }
            if (field.name === "id" && type.root) {
                this.report(statement.nameToken, "id is assigned by the cluster and is not written.");
                continue;
            }
            if (asBlock && !this.blockable(strip(field.type))) {
                this.report(statement.nameToken, `${statement.name} is an attribute: ${statement.name} = ${this.hint(strip(field.type))}.`);
                continue;
            }
            const entry = grouped.get(field.name) || {attrs: [], blocks: []};
            (asBlock ? entry.blocks : entry.attrs).push(statement);
            grouped.set(field.name, entry);
        }
        for (const field of type.fields) {
            if (field.name === "id" && type.root) continue;
            const entry = grouped.get(field.name);
            if (!entry) {
                const zero = this.zero(field.type);
                if (zero === null) this.report(owner.nameToken || owner, `${type.name} requires ${field.name}.`);
                else if (zero !== undefined) into[field.name] = zero;
                continue;
            }
            const value = this.fieldValue(field, entry.attrs, entry.blocks);
            if (value !== null && value !== undefined) into[field.name] = value;
        }
        return into;
    }

    fieldValue(field, attrs, blocks) {
        const shape = strip(field.type);
        if (attrs.length > 1) this.report(attrs[1].nameToken, `${field.name} is declared more than once.`);
        if (attrs.length && blocks.length) this.report(blocks[0].nameToken, `${field.name} is written either as an attribute or as blocks, not both.`);
        if (blocks.length) {
            if (shape.kind === "list" || shape.kind === "array") {
                const items = blocks.map(block => this.blockItem(block, shape.item));
                if (items.some(item => item === null)) return null;
                if (shape.kind === "array" && items.length !== shape.length) {
                    return this.report(blocks[0].nameToken, `${field.name} needs exactly ${shape.length} block${shape.length === 1 ? "" : "s"}.`);
                }
                return this.remember(items, blocks[0]);
            }
            if (blocks.length > 1) this.report(blocks[1].nameToken, `Only one ${field.name} block is allowed.`);
            if (shape.kind === "map") return this.mapEntries(blocks[0].body, shape, blocks[0], field.name);
            return this.blockItem(blocks[0], shape);
        }
        return this.expr(attrs[0].value, field.type);
    }

    blockItem(block, type) {
        const shape = strip(type);
        if (shape.kind === "record") return this.record(block.body, shape.type, block);
        if (shape.kind === "union") return this.unionBlock(block, shape);
        return this.report(block.nameToken, `${block.name} is an attribute: ${block.name} = ${this.hint(shape)}.`);
    }

    unionBlock(block, union) {
        const records = union.alternatives.filter(alt => alt.type.kind === "record");
        const names = records.map(alt => alt.codeName).join(", ");
        const chosen = [];
        for (const statement of block.body) {
            if (statement.kind !== "block") {
                this.report(statement.nameToken, `${block.name} chooses between ${names}; write one of them as a block, or give ${block.name} = value.`);
                continue;
            }
            const alt = records.find(candidate => candidate.codeName === statement.name);
            if (!alt) {
                this.report(statement.nameToken, `${block.name} has no block named ${statement.name}; expected one of ${names}.`);
                continue;
            }
            chosen.push([alt, statement]);
        }
        if (chosen.length > 1) this.report(chosen[1][1].nameToken, `${block.name} allows only one of ${names}.`);
        if (!chosen.length) return this.report(block.nameToken, `${block.name} requires one of ${names}.`);
        const [alt, statement] = chosen[0];
        const value = this.record(statement.body, alt.type.type, statement);
        return value ? this.remember({[alt.codeName]: value}, block) : null;
    }

    mapEntries(entries, mapType, owner, label) {
        const out = this.remember({}, owner);
        const seen = new Set();
        let failed = false;
        for (const entry of entries) {
            if (entry.kind !== "attribute") {
                this.report(entry.nameToken, `${label} entries are written as name = value.`);
                failed = true;
                continue;
            }
            const key = this.mapKey(entry, mapType.key);
            if (key === null) {
                failed = true;
                continue;
            }
            if (seen.has(key)) {
                this.report(entry.nameToken, `${label} entry ${JSON.stringify(entry.name)} is declared more than once.`);
                failed = true;
                continue;
            }
            seen.add(key);
            const value = this.expr(entry.value, mapType.value);
            if (value === null) {
                failed = true;
                continue;
            }
            Object.defineProperty(out, key, {value, enumerable: true, writable: true, configurable: true});
        }
        return failed ? null : out;
    }

    mapKey(entry, keyType) {
        if (keyType.name === "string") return entry.name;
        if (keyType.name === "bool") {
            if (entry.name !== "true" && entry.name !== "false") return this.report(entry.nameToken, "Map keys here are true or false.");
            return entry.name;
        }
        const number = Number(entry.name);
        const [min, max] = INTEGER_RANGES[keyType.name];
        if (!/^-?\d+$/.test(entry.name) || !Number.isSafeInteger(number) || number < min || number > max) {
            return this.report(entry.nameToken, `Map keys here are ${keyType.name} integers.`);
        }
        return String(number);
    }

    expr(node, type) {
        const shape = strip(type);
        if (shape.kind === "union") return this.unionValue(node, shape);
        if (node.kind === "call") return this.callAs(node, shape);
        switch (shape.kind) {
            case "prim": return this.literal(node, shape.name);
            case "id": return this.literal(node, shape.prim);
            case "enum": {
                const options = shape.type.cases.map(name => JSON.stringify(name)).join(", ");
                if (node.kind !== "string") return this.report(node, `Expected one of ${options}.`);
                if (!shape.type.cases.includes(node.value)) return this.report(node, `${JSON.stringify(node.value)} is not one of ${options}.`);
                return node.value;
            }
            case "key":
                return this.report(node, `Expected ${describeType(shape)} such as ${this.hint(shape)}.`);
            case "list": case "array": {
                if (node.kind !== "list") return this.report(node, `Expected ${describeType(shape)}: [ ... ].`);
                if (shape.kind === "array" && node.items.length !== shape.length) {
                    return this.report(node, `Expected exactly ${shape.length} item${shape.length === 1 ? "" : "s"}.`);
                }
                const out = this.remember([], node);
                let failed = false;
                for (const item of node.items) {
                    const value = this.expr(item, shape.item);
                    if (value === null) failed = true;
                    else out.push(value);
                }
                return failed ? null : out;
            }
            case "map":
                if (node.kind !== "object") return this.report(node, "Expected an object: { name = value ... }.");
                return this.mapEntries(node.entries, shape, node, "Object");
            case "record":
                if (node.kind !== "object") return this.report(node, `Expected a ${shape.type.name} block or object${this.hasFunction(shape) ? ` such as ${this.hint(shape)}` : ""}.`);
                return this.record(node.entries, shape.type, node);
            default:
                return this.report(node, `Unsupported type ${typeText(shape)}.`);
        }
    }

    literal(node, prim) {
        if (prim === "bool") {
            if (node.kind !== "boolean") return this.report(node, "Expected true or false.");
            return node.value;
        }
        if (isTextPrimitive(prim)) {
            if (node.kind !== "string") return this.report(node, "Expected a quoted string.");
            return node.value;
        }
        if (node.kind !== "number") return this.report(node, isFloatPrimitive(prim) ? "Expected a number." : "Expected an integer.");
        if (isFloatPrimitive(prim)) return node.value;
        const [min, max] = INTEGER_RANGES[prim];
        if (!Number.isInteger(node.value)) return this.report(node, "Expected an integer.");
        if (node.value < min || node.value > max) return this.report(node, `Expected an integer between ${min} and ${max}.`);
        return node.value;
    }

    hasFunction(type) {
        return [...this.functions.values()].some(fn => typeEquals(fn.returns, type));
    }

    hint(type) {
        const fns = [...this.functions.values()].filter(fn => typeEquals(fn.returns, type));
        if (fns.length) return fns.map(callHint).join(" or ");
        if (type.kind === "union") return this.unionOptions(type);
        if (type.kind === "record") return `${type.type.codeName} { ... }`;
        return describeType(type);
    }

    unionOptions(union) {
        return union.alternatives.map(alt => {
            if (alt.type.kind === "record") {
                const direct = [...this.functions.values()].filter(fn => typeEquals(fn.returns, alt.type));
                if (direct.length) return direct.map(callHint).join(" or ");
                const wrapped = this.wrapField(alt);
                if (wrapped) {
                    const inner = [...this.functions.values()].filter(fn => typeEquals(fn.returns, strip(wrapped.type)));
                    if (inner.length) return inner.map(callHint).join(" or ");
                    if (strip(wrapped.type).kind === "prim") return describeType(strip(wrapped.type));
                }
                return `${alt.codeName} { ... }`;
            }
            return this.hint(alt.type);
        }).join(", ");
    }

    wrapField(alt) {
        if (alt.type.kind !== "record") return null;
        const fields = alt.type.type.fields.filter(field => !(field.name === "id" && alt.type.type.root));
        const carriers = fields.filter(field => field.type.kind !== "optional" && !isCollection(field.type));
        if (carriers.length !== 1) return null;
        return carriers[0];
    }

    wrappable(union, valueType) {
        return union.alternatives
            .map(alt => ({alt, field: this.wrapField(alt)}))
            .filter(({field}) => field && typeEquals(strip(field.type), valueType));
    }

    wrap(alt, field, value, node) {
        const record = this.zero({kind: "record", type: alt.type.type}, field.name) || {};
        record[field.name] = value;
        return this.remember({[alt.codeName]: this.remember(record, node)}, node);
    }

    unionValue(node, union) {
        const options = () => this.unionOptions(union);
        if (node.kind === "call") {
            const result = this.call(node);
            if (!result) return null;
            if (typeEquals(result.type, union)) return result.value;
            const direct = union.alternatives.filter(alt => typeEquals(alt.type, result.type));
            if (direct.length === 1) return this.remember({[direct[0].codeName]: result.value}, node);
            const wrapped = this.wrappable(union, result.type);
            if (wrapped.length === 1) return this.wrap(wrapped[0].alt, wrapped[0].field, result.value, node);
            if (wrapped.length > 1) return this.report(node, `${node.name}() fits ${wrapped.map(({alt}) => alt.codeName).join(" and ")}; write the block form to choose.`);
            return this.report(node, `${node.name}() yields ${typeText(result.type)}, which is not accepted here; expected ${options()}.`);
        }
        if (node.kind === "object") {
            const records = union.alternatives.filter(alt => alt.type.kind === "record");
            if (records.length === 1) {
                const value = this.record(node.entries, records[0].type.type, node);
                return value ? this.remember({[records[0].codeName]: value}, node) : null;
            }
            return this.report(node, records.length
                ? `An object is ambiguous here; write a block naming one of ${records.map(alt => alt.codeName).join(", ")}.`
                : `Expected ${options()}.`);
        }
        const literalType = node.kind === "string" ? {kind: "prim", name: "string"}
            : node.kind === "number" ? {kind: "prim", name: Number.isInteger(node.value) ? "i64" : "f64"}
                : node.kind === "boolean" ? {kind: "prim", name: "bool"} : null;
        if (!literalType) return this.report(node, `Expected ${options()}.`);
        const accepts = type => type.kind === "prim" && (
            (literalType.name === "string" && isTextPrimitive(type.name))
            || (literalType.name === "bool" && type.name === "bool")
            || (literalType.name === "i64" && (isIntegerPrimitive(type.name) || isFloatPrimitive(type.name)))
            || (literalType.name === "f64" && isFloatPrimitive(type.name))
        );
        const direct = union.alternatives.filter(alt => accepts(alt.type) || (alt.type.kind === "enum" && node.kind === "string" && alt.type.type.cases.includes(node.value)));
        if (direct.length === 1) {
            const value = this.expr(node, direct[0].type);
            return value === null ? null : this.remember({[direct[0].codeName]: value}, node);
        }
        if (direct.length > 1) return this.report(node, `The value fits ${direct.map(alt => alt.codeName).join(" and ")}; write the block form to choose.`);
        const wrapped = union.alternatives
            .map(alt => ({alt, field: this.wrapField(alt)}))
            .filter(({field}) => field && accepts(strip(field.type)));
        if (wrapped.length === 1) {
            const value = this.expr(node, wrapped[0].field.type);
            return value === null ? null : this.wrap(wrapped[0].alt, wrapped[0].field, value, node);
        }
        if (wrapped.length > 1) return this.report(node, `The value fits ${wrapped.map(({alt}) => alt.codeName).join(" and ")}; write the block form to choose.`);
        return this.report(node, `Expected ${options()}.`);
    }

    call(node) {
        const fn = this.functions.get(node.name);
        if (!fn) return this.report(node.nameToken, `Unknown function ${node.name}().`);
        const required = fn.params.filter(param => !param.optional).length;
        if (node.args.length < required || node.args.length > fn.params.length) {
            return this.report(node, `${callHint(fn)} takes ${required === fn.params.length ? required : `${required} to ${fn.params.length}`} argument${fn.params.length === 1 ? "" : "s"}.`);
        }
        const args = [];
        let failed = false;
        node.args.forEach((arg, index) => {
            const value = this.expr(arg, fn.params[index].type);
            if (value === null) failed = true;
            args.push(value);
        });
        if (failed) return null;
        const before = this.diagnostics.length;
        const value = fn.impl(args, {
            node,
            args: node.args,
            root: this.root,
            report: (message, where = node) => this.report(where, message),
        });
        if (value === null || value === undefined) {
            if (this.diagnostics.length === before) this.report(node, `${node.name}() could not be resolved.`);
            return null;
        }
        this.remember(value, node);
        if (!this.checkValue(value, fn.returns, node, `${node.name}()`)) return null;
        return {value, type: fn.returns};
    }

    callAs(node, expected) {
        const result = this.call(node);
        if (!result) return null;
        if (!typeEquals(result.type, expected)) {
            return this.report(node, `${node.name}() yields ${typeText(result.type)}; expected ${describeType(expected)}.`);
        }
        return result.value;
    }

    zero(type, skipField = null) {
        switch (type.kind) {
            case "optional": return undefined;
            case "prim":
                if (type.name === "bool") return false;
                if (isTextPrimitive(type.name)) return "";
                return 0;
            case "enum": return type.type.cases[0];
            case "list": return [];
            case "map": return {};
            case "array": {
                const item = this.zero(type.item);
                if (item === null || item === undefined) return null;
                return Array.from({length: type.length}, () => structuredClone(item));
            }
            case "record": {
                const out = {};
                for (const field of type.type.fields) {
                    if (field.name === skipField || (field.name === "id" && type.type.root)) continue;
                    const value = this.zero(field.type);
                    if (value === null) return null;
                    if (value !== undefined) out[field.name] = value;
                }
                return out;
            }
            default: return null;
        }
    }

    checkValue(value, type, node, label) {
        const fail = message => { this.report(node, `${label} produced ${message}.`); return false; };
        switch (type.kind) {
            case "optional": return value === undefined || value === null || this.checkValue(value, type.item, node, label);
            case "prim": case "id": case "key": {
                const prim = type.kind === "prim" ? type.name : type.prim;
                if (prim === "bool") return typeof value === "boolean" || fail(`${JSON.stringify(value)} instead of a boolean`);
                if (isTextPrimitive(prim)) return typeof value === "string" || fail(`${JSON.stringify(value)} instead of a string`);
                if (isFloatPrimitive(prim)) return typeof value === "number" && Number.isFinite(value) || fail(`${JSON.stringify(value)} instead of a number`);
                const [min, max] = INTEGER_RANGES[prim];
                return (Number.isSafeInteger(value) && value >= min && value <= max) || fail(`${JSON.stringify(value)} instead of ${prim === "u64" && type.kind !== "prim" ? "an id" : `a ${prim}`}`);
            }
            case "enum": return type.type.cases.includes(value) || fail(`${JSON.stringify(value)}, which is not one of ${type.type.cases.join(", ")}`);
            case "list": case "array":
                if (!Array.isArray(value)) return fail("a non-list");
                if (type.kind === "array" && value.length !== type.length) return fail(`${value.length} items instead of ${type.length}`);
                return value.every((item, index) => this.checkValue(item, type.item, node, `${label}[${index}]`));
            case "map":
                if (!value || typeof value !== "object" || Array.isArray(value)) return fail("a non-object");
                return Object.entries(value).every(([key, item]) => this.checkValue(item, type.value, node, `${label}.${key}`));
            case "union": {
                if (!value || typeof value !== "object" || Array.isArray(value)) return fail(`a non-object for ${typeText(type)}`);
                const keys = Object.keys(value);
                const alt = keys.length === 1 ? type.alternatives.find(candidate => candidate.codeName === keys[0]) : null;
                if (!alt) return fail(`${keys.length ? keys.join(", ") : "nothing"} where exactly one of ${type.alternatives.map(candidate => candidate.codeName).join(", ")} is needed`);
                return this.checkValue(value[keys[0]], alt.type, node, `${label}.${keys[0]}`);
            }
            case "record": {
                if (!value || typeof value !== "object" || Array.isArray(value)) return fail(`a non-object for ${type.type.name}`);
                let ok = true;
                for (const key of Object.keys(value)) {
                    if (!type.type.fieldMap.has(key)) ok = fail(`unknown field ${key} on ${type.type.name}`);
                }
                for (const field of type.type.fields) {
                    const item = value[field.name];
                    if (item === undefined || item === null) {
                        if (field.type.kind !== "optional" && !(field.name === "id" && type.type.root)) ok = fail(`${type.type.name} without ${field.name}`);
                        continue;
                    }
                    if (!this.checkValue(item, field.type, node, `${label}.${field.name}`)) ok = false;
                }
                return ok;
            }
            default: return fail(`an unsupported ${type.kind}`);
        }
    }
}

function rootOf(schema, rootName) {
    const root = schema.roots.get(rootName);
    if (!root) throw new Error(`${rootName} is not a root type.`);
    return root;
}

export function evaluate(schema, rootName, text, env = {}) {
    const root = rootOf(schema, rootName);
    const source = String(text ?? "");
    const parsed = parseCode(source);
    const diagnostics = parsed.diagnostics;
    if (!parsed.ast) return {instance: null, diagnostics, nodeOf: () => null};
    let ast = parsed.ast;
    if (env.rewrite) ast = env.rewrite(ast, (node, message) => { diagnostics.push(at(node, message)); }) || ast;
    const evaluator = new Evaluator(schema, env.functions || new Map(), diagnostics);
    const instance = evaluator.document(ast, root);
    const failed = diagnostics.some(item => item.severity === "error");
    return {
        instance: failed ? null : instance,
        diagnostics,
        nodeOf: value => evaluator.nodes.get(value) ?? null,
    };
}

export function validateInstance(schema, rootName, instance) {
    const root = rootOf(schema, rootName);
    const diagnostics = [];
    const evaluator = new Evaluator(schema, new Map(), diagnostics);
    const ok = evaluator.checkValue(instance, {kind: "record", type: root}, {from: 0, to: 0}, root.codeName);
    return {ok: ok && !diagnostics.length, diagnostics};
}
