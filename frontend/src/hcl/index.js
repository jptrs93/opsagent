import {
    delimitedIndent,
    foldInside,
    foldNodeProp,
    indentNodeProp,
    LanguageSupport,
    LRLanguage,
} from "@codemirror/language";
import {styleTags, tags} from "@lezer/highlight";
import {parser} from "./parser.js";

export const deploymentHclLanguage = LRLanguage.define({
    name: "deploymenthcl",
    parser: parser.configure({
        props: [
            indentNodeProp.add({
                BlockBody: delimitedIndent({closing: "}"}),
                ObjectValue: delimitedIndent({closing: "}"}),
                ListValue: delimitedIndent({closing: "]"}),
            }),
            foldNodeProp.add({
                BlockBody: foldInside,
                ObjectValue: foldInside,
                ListValue: foldInside,
            }),
            styleTags({
                "Attribute/Identifier": tags.propertyName,
                "ObjectAttribute/Identifier": tags.propertyName,
                "Block/Identifier": tags.keyword,
                "FunctionCall/Identifier": tags.function(tags.variableName),
                BoolLit: tags.bool,
                NumberLit: tags.number,
                StringLit: tags.string,
                LineComment: tags.lineComment,
                BlockComment: tags.blockComment,
                "( )": tags.paren,
                "[ ]": tags.squareBracket,
                "{ }": tags.brace,
                "=": tags.definitionOperator,
                ",": tags.separator,
            }),
        ],
    }),
    languageData: {
        commentTokens: {line: "#", block: {open: "/*", close: "*/"}},
        closeBrackets: {brackets: ["(", "[", "{", '"']},
    },
});

export function deploymentHcl() {
    return new LanguageSupport(deploymentHclLanguage);
}
