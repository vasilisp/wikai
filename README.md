# wikai: Git-powered note-taking with AI superpowers

`wikai` is an experimental note-taking app (a personal Wiki) that combines the
power of `git` with AI.

Notes are stored as plain Markdown files in a `git` repository. On top of this,
`wikai` provides AI-powered semantic search and editing capabilities.

It retains the distributed, offline-friendly nature of `git`, while layering in
intelligent features. Semantic search is implemented using vector embeddings,
which are stored via `git notes` and loaded into memory at startup. This means
you can run wikai on top of any cloned repository without needing a separate
database. Teams can collaborate on notes the same way they collaborate on
code—with branches, commits, and pull requests.

The AI layer also brings extra convenience: you can create or update notes via a
chatbot-style interface that automatically proofreads content and applies
Markdown formatting.