# Documentation

## Structure

```
CLAUDE.md               High level summary information for LLM agents. Top-level project structure summary and a list of all files under docs and what concern they address.
docs/
  documentation.md       This file. Documentation design and organisation.
  engineering/           System design, architecture, and cross-cutting patterns. Individual topic files live here.
  product/               Product-facing concepts: features, lifecycle, and workflows. Individual topic files live here.
```

Individual documentation files go under `engineering/` or `product/` — one file per topic. CLAUDE.md lists each file and the concern it addresses; this file does not duplicate that index. Examples of such files are `engineering/<some-subsystem>.md` or `product/<some-feature>.md`.

## Writing style

- Use plain, precise, and unambiguous language.
- Use a hierarchical structure with clearly defined sections and subsections.
- Avoid duplication; reference existing sections.
- Use consistent terminology with fixed meanings.
- Use declarative sentences.
- Remove unnecessary words.
- Avoid subjective language.
- Avoid intensifiers and qualifiers.
- Present information from general to specific.
- Express one idea per statement.
