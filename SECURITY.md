# Security policy

## Supported versions

Security fixes go into the latest release. Please update to it before reporting.

## Reporting a vulnerability

Please **don't** open a public issue for security problems. Instead, use GitHub's private reporting:

1. Go to the [Security tab](https://github.com/ItsFurai/Seek-CLI/security) of this repository.
2. Click **Report a vulnerability**.
3. Describe the problem, how to reproduce it, and what an attacker could do with it.

You should get a reply within a week. Once a fix is released, you'll be credited in the release notes unless you'd rather stay anonymous.

## Scope

seek reads file names, file metadata and file contents on your machine, and only stores its index locally. It makes no network connections. Things worth reporting include ways to make seek run code, read files outside what you asked it to search, or corrupt files when opening or revealing them.
