# config/

Holds the age SSH ed25519 keypair used to sign/encrypt registration-link
tokens. The key files are **gitignored** — never commit them.

Generate them with:

```sh
make keys
```

This creates:

- `dday_ed25519`     — private key (bot/server decrypts & the recipient is derived from it)
- `dday_ed25519.pub` — public key (recipient used to encrypt tokens)

Point `AGE_PUB` / `AGE_KEY` in `matrix.env` at these files.
