import { Compartment, EditorState, type Extension } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language';
import { javascript } from '@codemirror/lang-javascript';
import { protobuf } from '@codemirror/legacy-modes/mode/protobuf';
import { json } from '@codemirror/legacy-modes/mode/javascript';
import { tags } from '@lezer/highlight';

export type CodeLanguage = 'text' | 'json' | 'hcl' | 'protobuf' | 'typescript' | 'smithy';

export interface CodeVariant {
  label: string;
  language: CodeLanguage;
  code: string;
}

// Legacy modes ships Protobuf but not HCL or Smithy. These are display lexers,
// not validators; preserve multiline comments, text blocks, and HCL heredocs.
function configLanguage(name: 'hcl' | 'smithy') {
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
      if (stream.match('//') || (name === 'hcl' && stream.match('#'))) {
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
        // Keep the native caret and keyboard selection; readOnly rejects edits.
        EditorView.editable.of(true),
        syntax.of(languages[language]),
        accessibility.of(attributes(label)),
        syntaxHighlighting(HighlightStyle.define([
          { tag: tags.comment, color: '#94a3b8' },
          { tag: tags.keyword, color: '#c4b5fd' },
          { tag: tags.string, color: '#86efac' },
          { tag: [tags.number, tags.bool], color: '#fdba74' },
          { tag: tags.typeName, color: '#7dd3fc' },
          { tag: tags.meta, color: '#f0abfc' },
          { tag: tags.operator, color: '#93c5fd' },
        ])),
        EditorView.theme({
          '&': { backgroundColor: 'transparent', color: 'var(--code-ink, #e2e8f0)', fontSize: '13px' },
          '&.cm-focused': { outline: 'none' },
          '.cm-scroller': { overflowX: 'auto', fontFamily: 'inherit', lineHeight: '1.55' },
          '.cm-content': { padding: '15px 0', caretColor: '#7dd3fc' },
          '.cm-content:focus': { outline: 'none' },
          '.cm-line': { padding: '0 17px' },
          '.cm-content ::selection, .cm-content::selection': { backgroundColor: '#1e4976' },
        }, { dark: true }),
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
