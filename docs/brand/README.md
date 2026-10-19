# ncli brand

Purple first: `#5A189A` → `#7B3FE4` → `#9D4EDD`, magenta `#FF3DFF` only for
the cursor and goggle accents. Display type is Righteous (outlined in the
logo files, so no font is needed).

| File | Mark | Use |
|---|---|---|
| `ncli-logo.svg` | ring ostrich + wordmark, text follows light/dark | anywhere an `<img>` can follow the OS theme |
| `ncli-logo-on-dark.svg`, `ncli-logo-on-light.svg` | same, fixed text color | README header (`<picture>`), slides |
| `ncli-mark.svg`, `ncli-mark-512.png` | ring ostrich | docs header, dashboard header and sign-in, homepage footer, avatars |
| `ohstr-icon-192.png`, `favicon.ico`, `apple-touch-icon.png` | ohstr "O" ostrich, from ohstr.com | favicons and app icons: dashboard, relay homepage, docs site |
| `web/dashboard/src/brand/ncli-banner.svg` | ring and `>_` on `#0C0A14`, 3:1 | relay homepage banner fallback |
| `ncli-pixel.svg`, `ncli-pixel-512.png` | 8-bit ostrich | terminal banners, stickers |
| `ncli-social.png` | logo on `#0C0A14`, 1280×640 | GitHub social preview |

The ncli PNGs are rendered from the SVGs; the ohstr favicons are ohstr.com's
own. The dashboard and homepage bundle their own copies of
`ohstr-icon-192.png` and `ncli-mark.svg` from `web/dashboard/src/brand/`;
update both together.

The relay homepage keeps the ohstr favicon. Its hero banner is the relay's
NIP-11 `banner`, falling back to `web/dashboard/src/brand/ncli-banner.svg`.
