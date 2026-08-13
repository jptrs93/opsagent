// Regenerates parser.js and parser.terms.js from deploymentHcl.grammar.
// Usage: node src/hcl/generate.js
import {readFileSync, writeFileSync} from "node:fs";
import {buildParserFile} from "@lezer/generator";

const grammarUrl = new URL("./deploymentHcl.grammar", import.meta.url);
const {parser, terms} = buildParserFile(readFileSync(grammarUrl, "utf8"), {
    fileName: "deploymentHcl.grammar",
    moduleStyle: "es",
    warn: message => { throw new Error(message); },
});
writeFileSync(new URL("./parser.js", import.meta.url), parser);
writeFileSync(new URL("./parser.terms.js", import.meta.url), terms);
console.log("Wrote src/hcl/parser.js and src/hcl/parser.terms.js");
