import {parseSignature} from "./typeParser.js";
import {typeText} from "./schema.js";

export function defineFunctions(schema, specs) {
    const functions = new Map();
    for (const [name, spec] of Object.entries(specs)) {
        const parsed = parseSignature(spec.signature);
        const params = [];
        let seenOptional = false;
        for (const param of parsed.params) {
            const type = schema.resolve(param.type);
            if (!type) throw new Error(`Function ${name}: parameter ${param.name} has an unknown type.`);
            if (seenOptional && !param.optional) throw new Error(`Function ${name}: required parameter ${param.name} follows an optional one.`);
            seenOptional ||= param.optional;
            params.push({name: param.name, type, optional: param.optional});
        }
        const returns = schema.resolve(parsed.returns);
        if (!returns) throw new Error(`Function ${name}: unknown return type.`);
        if (typeof spec.impl !== "function") throw new Error(`Function ${name} has no implementation.`);
        functions.set(name, {name, params, returns, impl: spec.impl});
    }
    return functions;
}

export function callHint(fn) {
    return `${fn.name}(${fn.params.map(param => param.optional ? `[${param.name}]` : param.name).join(", ")})`;
}

export function fnSignature(fn) {
    return `${fn.name}(${fn.params.map(param => `${param.name}: ${typeText(param.type)}`).join(", ")}) -> ${typeText(fn.returns)}`;
}
