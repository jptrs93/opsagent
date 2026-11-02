import { Compartment, EditorState, type Extension } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language';
import { javascript } from '@codemirror/lang-javascript';
import { protobuf } from '@codemirror/legacy-modes/mode/protobuf';
import { json } from '@codemirror/legacy-modes/mode/javascript';
import { tags } from '@lezer/highlight';

export type CodeLanguage = 'text' | 'json' | 'hcl' | 'protobuf' | 'typescript' | 'smithy' | 'typedef';

export interface CodeVariant {
  label: string;
  language: CodeLanguage;
  code: string;
}

// Legacy modes ships Protobuf but not HCL or Smithy. These are display lexers,
// not validators; preserve multiline comments, text blocks, and HCL heredocs.
function configLanguage(name: 'hcl' | 'smithy' | 'typedef') {
  return StreamLanguage.define({
    name,
    startState: () => ({ blockComment: false, textBlock: false, heredoc: '' }),
    token(stream, state) {
      if (state.heredoc) {
        if (stream.string.trim() === state.heredoc) state.heredoc = '';
        stream.skipToEnd();
        return 'string';
      }
      if (state.blockComment || stream.match('/*')) {
        state.blockComment = true;
        if (stream.skipTo('*/')) {
          stream.match('*/');
          state.blockComment = false;
        } else stream.skipToEnd();
        return 'comment';
      }
      if (state.textBlock || (name === 'smithy' && stream.match('"""'))) {
        state.textBlock = true;
        if (stream.skipTo('"""')) {
          stream.match('"""');
          state.textBlock = false;
        } else stream.skipToEnd();
        return 'string';
      }
      if (stream.eatSpace()) return null;
      if (stream.match('//') || (name !== 'smithy' && stream.match('#'))) {
        stream.skipToEnd();
        return 'comment';
      }
      if (name === 'hcl') {
        const heredoc = stream.match(/^<<-?([A-Za-z_][\w-]*)/);
        if (heredoc && typeof heredoc !== 'boolean') {
          state.heredoc = heredoc[1];
          return 'string';
        }
      }
      if (stream.match(/^"(?:[^"\\]|\\.)*(?:"|$)/)) return 'string';
      if (stream.match(/^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/)) return 'number';
      if (stream.match(/^[@$][A-Za-z_][\w.#]*/)) return 'meta';
      if (name === 'typedef') {
        if (stream.match(/^(?:type|enum|id|root|mat|map)\b/)) return 'keyword';
        if (stream.match(/^(?:bool|string|bytes|uuid|i32|i64|u8|u32|u64|f32|f64)\b/)) return 'typeName';
        if (stream.match(/^(?:->|\||\?|\[\d*\])/)) return 'operator';
        if (stream.match(/^[A-Z][\w]*/)) return 'typeName';
      }
      if (stream.match(/^(?:namespace|use|structure|list|map|blob|bigInteger|long|double|string|boolean|integer|timestamp|document|union|enum|intEnum|service|operation|resource|apply|metadata|for|in|if|true|false|null)\b/)) return 'keyword';
      if (stream.match(/^[A-Za-z_][\w.-]*/)) return 'variableName';
      stream.next();
      return 'punctuation';
    },
  });
}

const languages: Record<CodeLanguage, Extension> = {
  text: [],
  json: StreamLanguage.define(json),
  hcl: configLanguage('hcl'),
  protobuf: StreamLanguage.define(protobuf),
  typescript: javascript({ typescript: true }),
  smithy: configLanguage('smithy'),
  typedef: configLanguage('typedef'),
};

// Imported by CodeBlock only after mount, keeping the editor out of SSR.
export function createCodeEditor(parent: HTMLElement, code: string, language: CodeLanguage, label: string) {
  const syntax = new Compartment();
  const accessibility = new Compartment();
  const attributes = (name: string) => EditorView.contentAttributes.of({
    'aria-label': name,
    'aria-readonly': 'true',
    'aria-multiline': 'true',
    role: 'textbox',
    spellcheck: 'false',
  });
  const view = new EditorView({
    parent,
    state: EditorState.create({
      doc: code,
      extensions: [
        EditorState.readOnly.of(true),
        EditorView.editable.of(false),
        syntax.of(languages[language]),
        accessibility.of(attributes(label)),
        syntaxHighlighting(HighlightStyle.define([
          { tag: tags.comment, color: 'var(--tok-comment)' },
          { tag: tags.keyword, color: 'var(--tok-keyword)' },
          { tag: tags.string, color: 'var(--tok-string)' },
          { tag: [tags.number, tags.bool], color: 'var(--tok-number)' },
          { tag: tags.typeName, color: 'var(--tok-type)' },
          { tag: tags.meta, color: 'var(--tok-meta)' },
          { tag: tags.operator, color: 'var(--tok-operator)' },
        ])),
        EditorView.theme({
          '&': { backgroundColor: 'transparent', color: 'var(--code-ink)', fontSize: '13px' },
          '&.cm-focused': { outline: 'none' },
          '.cm-scroller': { overflowX: 'auto', fontFamily: 'inherit', lineHeight: '1.55' },
          '.cm-content': { padding: '14px 0' },
          '.cm-content:focus': { outline: 'none' },
          '.cm-line': { padding: '0 16px' },
          '.cm-content ::selection, .cm-content::selection': { backgroundColor: 'var(--code-select)' },
        }),
      ],
    }),
  });
  let currentLanguage = language;
  let currentLabel = label;
  return {
    update(nextCode: string, nextLanguage: CodeLanguage, nextLabel: string) {
      const changed = view.state.doc.toString() !== nextCode;
      const effects = [];
      if (nextLanguage !== currentLanguage) effects.push(syntax.reconfigure(languages[nextLanguage]));
      if (nextLabel !== currentLabel) effects.push(accessibility.reconfigure(attributes(nextLabel)));
      if (changed || effects.length) {
        view.dispatch({
          changes: changed ? { from: 0, to: view.state.doc.length, insert: nextCode } : undefined,
          selection: changed ? { anchor: 0 } : undefined,
          effects,
        });
        if (changed) view.scrollDOM.scrollLeft = 0;
      }
      currentLanguage = nextLanguage;
      currentLabel = nextLabel;
    },
    destroy: () => view.destroy(),
  };
}
