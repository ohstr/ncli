# ncli brand

Purple first: `#5A189A` → `#7B3FE4` → `#9D4EDD`, magenta `#FF3DFF` only for
the cursor and goggle accents. Display type is Righteous (outlined in the
logo files, so no font is needed).

| File | Mark | Use |
|---|---|---|
| `ncli-logo.svg` | ring ostrich + wordmark, text follows light/dark | anywhere an `<img>` can follow the OS theme |
| `ncli-logo-on-dark.svg`, `ncli-logo-on-light.svg` | same, fixed text color | README header (`<picture>`), slides |
| `ncli-mark.svg`, `ncli-mark-512.png` | ring ostrich | docs header, dashboard header and sign-in, homepage footer, avatars |
| `ncli-icon.svg`, `ncli-icon-{16,32,48,192,512}.png`, `favicon.ico`, `apple-touch-icon.png` | `n_` tile | favicons and app icons: dashboard, relay homepage fallback, docs site |
| `ncli-pixel.svg`, `ncli-pixel-512.png` | 8-bit ostrich | terminal banners, stickers |
| `ncli-social.png` | logo on `#0C0A14`, 1280×640 | GitHub social preview |

PNG and ICO files are rendered from the SVGs. The dashboard and homepage
bundle their own copies of `ncli-icon.svg` and `ncli-mark.svg` from
`web/dashboard/src/brand/`; update both together.

The relay homepage swaps the favicon for the relay's NIP-11 `icon` (or the
operator's picture) once known, since that page belongs to the operator.
