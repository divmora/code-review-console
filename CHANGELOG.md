# Changelog

## [0.12.0](https://github.com/divmora/code-review-console/compare/v0.11.0...v0.12.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* hooks still pointing at /webhooks/<platform> get 404. Add a webhook under Code hosts -> Manage webhooks and point the hook at its URL and secret before upgrading.

### Features

* Impact page showing what the console has done for this installation ([#105](https://github.com/divmora/code-review-console/issues/105)) ([22eba4e](https://github.com/divmora/code-review-console/commit/22eba4eecfb4cb851ae9808da084ba771913f1f1))
* remove the shared webhook URLs ([#99](https://github.com/divmora/code-review-console/issues/99)) ([f57bb1c](https://github.com/divmora/code-review-console/commit/f57bb1c7fcd658855bc9345c57536080b1c0fe67))
* store review scores and show them on Reviews, Repositories, the dashboard and /metrics ([#103](https://github.com/divmora/code-review-console/issues/103)) ([aa47c71](https://github.com/divmora/code-review-console/commit/aa47c7137a28b96139e156c0dc0dbfa4184a5045)), closes [#98](https://github.com/divmora/code-review-console/issues/98)


### Bug Fixes

* **hosts:** every replica picks up host and webhook changes without a restart ([#104](https://github.com/divmora/code-review-console/issues/104)) ([1599cea](https://github.com/divmora/code-review-console/commit/1599cea8c346de3e6ffc5733ac568820ef2d5f7c)), closes [#101](https://github.com/divmora/code-review-console/issues/101)

## [0.11.0](https://github.com/divmora/code-review-console/compare/v0.10.0...v0.11.0) (2026-11-23)


### Features

* admin, operator and viewer roles, with approval for operators' sensitive changes ([#82](https://github.com/divmora/code-review-console/issues/82)) ([a5f3562](https://github.com/divmora/code-review-console/commit/a5f356273b7f5714b3b82f06ee5b46621201c226)), closes [#68](https://github.com/divmora/code-review-console/issues/68)


### Bug Fixes

* **admin:** sign sessions out after inactivity ([#94](https://github.com/divmora/code-review-console/issues/94)) ([ac53abc](https://github.com/divmora/code-review-console/commit/ac53abc07733584cedd12a308c57781e00b32922)), closes [#87](https://github.com/divmora/code-review-console/issues/87)
* **clientip:** count proxy hops; warn on wide trusted ranges; fix the advice ([#85](https://github.com/divmora/code-review-console/issues/85)) ([0743045](https://github.com/divmora/code-review-console/commit/07430456679f5f6b9454068c01bca2e98d0fb598)), closes [#83](https://github.com/divmora/code-review-console/issues/83)
* no directory listing under /admin/assets/; /healthz shows only the status ([#91](https://github.com/divmora/code-review-console/issues/91)) ([1dff539](https://github.com/divmora/code-review-console/commit/1dff5390eb0466a8d9cb5fdffdcad99b2409c129)), closes [#89](https://github.com/divmora/code-review-console/issues/89)
* **queue:** waiting jobs survive restarts, upgrades and evictions ([#96](https://github.com/divmora/code-review-console/issues/96)) ([26d1aca](https://github.com/divmora/code-review-console/commit/26d1aca58c42e97a1d3942dc04a0ad1ac59fd77c))
* **ui:** Settings no longer crashes when no extra job variables are set ([#97](https://github.com/divmora/code-review-console/issues/97)) ([5b41495](https://github.com/divmora/code-review-console/commit/5b41495f22eac2c9a6172b6800dd2790933d920f)), closes [#95](https://github.com/divmora/code-review-console/issues/95)
* **webhooks:** a signing token makes a GitLab webhook signature-only ([#93](https://github.com/divmora/code-review-console/issues/93)) ([7d05cec](https://github.com/divmora/code-review-console/commit/7d05cec06ff36391d302704c54140df914610c08)), closes [#86](https://github.com/divmora/code-review-console/issues/86)

## [0.10.0](https://github.com/divmora/code-review-console/compare/v0.9.0...v0.10.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* database.url and CODEREVIEW_CONSOLE_SECRET_KEY are required. Model profiles from the config file are copied into the admin panel once and ignored afterwards; move a literal api_key to an environment variable first.

### Features

* **audit:** search, paging and export; before/after and named secret changes ([#80](https://github.com/divmora/code-review-console/issues/80)) ([6c4c8be](https://github.com/divmora/code-review-console/commit/6c4c8be0042bebbdf38bb2a77728de1ece17569c)), closes [#65](https://github.com/divmora/code-review-console/issues/65) [#67](https://github.com/divmora/code-review-console/issues/67)
* keep configuration in the admin panel; require a database and secret key ([#64](https://github.com/divmora/code-review-console/issues/64)) ([efd703f](https://github.com/divmora/code-review-console/commit/efd703f27378cea027aa9fc5aae58e78df0ac5e0))
* manage the queue, review engine, bot name and dashboard token in the admin panel ([#74](https://github.com/divmora/code-review-console/issues/74)) ([05a62b1](https://github.com/divmora/code-review-console/commit/05a62b135a0a82a2fc4f0ba4f0918ab6d399793f))
* Prometheus /metrics, with access chosen in the admin panel ([#79](https://github.com/divmora/code-review-console/issues/79)) ([f23530d](https://github.com/divmora/code-review-console/commit/f23530db9d1c1de11198721264dbff14515a3238))
* record client IP and browser on sign-ins, changes, sessions and webhooks ([#78](https://github.com/divmora/code-review-console/issues/78)) ([966234e](https://github.com/divmora/code-review-console/commit/966234e606e5475b399585db52f5688c258c512d)), closes [#66](https://github.com/divmora/code-review-console/issues/66)
* run without a config file; startup settings from environment variables ([#75](https://github.com/divmora/code-review-console/issues/75)) ([7d077b1](https://github.com/divmora/code-review-console/commit/7d077b16b77bbc0a4d26871bde7869b789299928))
* warn before the license or a code host token expires ([#77](https://github.com/divmora/code-review-console/issues/77)) ([6713fd5](https://github.com/divmora/code-review-console/commit/6713fd5c00597ead4563ce8fd0c740ab4534bf9b)), closes [#72](https://github.com/divmora/code-review-console/issues/72)


### Bug Fixes

* **dashboard:** admins only by default; choose token or public access in the panel ([#76](https://github.com/divmora/code-review-console/issues/76)) ([d995872](https://github.com/divmora/code-review-console/commit/d995872f73cc51ee82221d753ccd8b3e2c394134))
* **ui:** Code hosts check results that fit and don't look verified; polish ([#81](https://github.com/divmora/code-review-console/issues/81)) ([a1d817e](https://github.com/divmora/code-review-console/commit/a1d817eff5831f527036b14a93a9a439fe8d4df7)), closes [#70](https://github.com/divmora/code-review-console/issues/70) [#71](https://github.com/divmora/code-review-console/issues/71)

## [0.9.0](https://github.com/divmora/code-review-console/compare/v0.8.0...v0.9.0) (2026-11-23)


### Features

* **kubernetes:** limit review pods' disk and /tmp ([#62](https://github.com/divmora/code-review-console/issues/62)) ([8cc2812](https://github.com/divmora/code-review-console/commit/8cc2812a5bbd25bf033ad88f7de514fc682ebd7c)), closes [#30](https://github.com/divmora/code-review-console/issues/30)


### Bug Fixes

* **admin:** redirect /admin/, show "page not found", ask before signing out ([#61](https://github.com/divmora/code-review-console/issues/61)) ([1ae41bf](https://github.com/divmora/code-review-console/commit/1ae41bfa2f1e7812bd6d70a18726f698927bc05b)), closes [#60](https://github.com/divmora/code-review-console/issues/60)

## [0.8.0](https://github.com/divmora/code-review-console/compare/v0.7.1...v0.8.0) (2026-11-22)


### Features

* add the Code Review logo (code brackets with a check) ([#53](https://github.com/divmora/code-review-console/issues/53)) ([7268d90](https://github.com/divmora/code-review-console/commit/7268d90b8827218a4e313ffd2ea701218ed8d924))


### Bug Fixes

* **commands:** only authors who can push run model commands; rate limit them ([#58](https://github.com/divmora/code-review-console/issues/58)) ([f4e55f2](https://github.com/divmora/code-review-console/commit/f4e55f28b06fd9daa23ed7eba8117f14c20292fa)), closes [#28](https://github.com/divmora/code-review-console/issues/28)
* **webhooks:** share replay protection across replicas and restarts ([#59](https://github.com/divmora/code-review-console/issues/59)) ([3cbab9e](https://github.com/divmora/code-review-console/commit/3cbab9e1faa595a1dec820d9ae12cfcaf1baa02d)), closes [#34](https://github.com/divmora/code-review-console/issues/34)

## [0.7.1](https://github.com/divmora/code-review-console/compare/v0.7.0...v0.7.1) (2026-11-22)


### Bug Fixes

* **admin:** recheck sessions against the sign-in rules; sign sessions out ([#48](https://github.com/divmora/code-review-console/issues/48)) ([0c01d92](https://github.com/divmora/code-review-console/commit/0c01d925539b67690be0495fa93e2e4da3484d65)), closes [#35](https://github.com/divmora/code-review-console/issues/35)
* **check-config:** print the allowlist saved in the admin panel ([#52](https://github.com/divmora/code-review-console/issues/52)) ([737b3b7](https://github.com/divmora/code-review-console/commit/737b3b7fbda75e61b331128a144da0d33a273525)), closes [#50](https://github.com/divmora/code-review-console/issues/50)
* **learnings:** only authors who can push activate learnings; wait by default ([#51](https://github.com/divmora/code-review-console/issues/51)) ([5ff4dbd](https://github.com/divmora/code-review-console/commit/5ff4dbd17deb3c29f6df92c532394d7bf3f90d74)), closes [#32](https://github.com/divmora/code-review-console/issues/32)

## [0.7.0](https://github.com/divmora/code-review-console/compare/v0.6.0...v0.7.0) (2026-11-22)


### Features

* **admin:** pick the bot version from the image repository's tags ([#44](https://github.com/divmora/code-review-console/issues/44)) ([3ea94b7](https://github.com/divmora/code-review-console/commit/3ea94b76034a3f0e9df1fecd79115b9c21f930d5))


### Bug Fixes

* **check-config:** run with the settings saved in the admin panel ([#47](https://github.com/divmora/code-review-console/issues/47)) ([1b75e79](https://github.com/divmora/code-review-console/commit/1b75e79edbf845123f6f2b9217197d756eaf9db1)), closes [#45](https://github.com/divmora/code-review-console/issues/45)
* **executor:** split repo:tag@digest images into repository and version ([#43](https://github.com/divmora/code-review-console/issues/43)) ([feb85d3](https://github.com/divmora/code-review-console/commit/feb85d308b4eb53c80b40afd96184b13902a1472)), closes [#41](https://github.com/divmora/code-review-console/issues/41)
* **kubernetes:** read review results with log lines spliced into them ([#40](https://github.com/divmora/code-review-console/issues/40)) ([7d1320e](https://github.com/divmora/code-review-console/commit/7d1320e3f6865023cf4467a346f304eeac496709))
* **webhooks:** authenticate before reading or recording the payload ([#46](https://github.com/divmora/code-review-console/issues/46)) ([edf7ea3](https://github.com/divmora/code-review-console/commit/edf7ea3196869124a16dff392ae244d92cc5d392)), closes [#33](https://github.com/divmora/code-review-console/issues/33)

## [0.6.0](https://github.com/divmora/code-review-console/compare/v0.5.0...v0.6.0) (2026-11-22)


### Features

* **admin:** manage executor settings in the console ([#38](https://github.com/divmora/code-review-console/issues/38)) ([d821bda](https://github.com/divmora/code-review-console/commit/d821bdaa00ceea06719f99b90335abea71fa09c4))
* **admin:** manage model profiles and the default model in the console ([#37](https://github.com/divmora/code-review-console/issues/37)) ([60edf06](https://github.com/divmora/code-review-console/commit/60edf067f3406b054adacac23f2ea80a21755f66))
* **license:** manage the license in the admin panel; start without one ([#31](https://github.com/divmora/code-review-console/issues/31)) ([029ff2e](https://github.com/divmora/code-review-console/commit/029ff2e391a2d63c12586851ed661c373fb777b0))

## [0.5.0](https://github.com/divmora/code-review-console/compare/v0.4.0...v0.5.0) (2026-11-22)


### Features

* **admin:** guided setup checklist ([#26](https://github.com/divmora/code-review-console/issues/26)) ([faa4f78](https://github.com/divmora/code-review-console/commit/faa4f781fe7871ead0ff4a443957be2b1861cec1))
* **admin:** warn when a host's webhooks never carry comment events ([#27](https://github.com/divmora/code-review-console/issues/27)) ([b8ed340](https://github.com/divmora/code-review-console/commit/b8ed340c7ddf6553a9558dd300e7b8431cff3d0f)), closes [#25](https://github.com/divmora/code-review-console/issues/25)
* **hosts:** import/export commands; deprecate hosts in the config file ([#24](https://github.com/divmora/code-review-console/issues/24)) ([48fca13](https://github.com/divmora/code-review-console/commit/48fca1366a1fdee5ce1753c08f43ee400c20e7d8))
* **webhooks:** per-webhook URLs with their own secrets ([#22](https://github.com/divmora/code-review-console/issues/22)) ([482996f](https://github.com/divmora/code-review-console/commit/482996f6cbdc2ce55b959a838c274e26c0940f14))

## [0.4.0](https://github.com/divmora/code-review-console/compare/v0.3.1...v0.4.0) (2026-11-22)


### Features

* **kubernetes:** scheduling options and a CA bundle for review pods ([#21](https://github.com/divmora/code-review-console/issues/21)) ([0c51977](https://github.com/divmora/code-review-console/commit/0c519771cc2a2754bff1e2813c8360e95be03f95)), closes [#16](https://github.com/divmora/code-review-console/issues/16) [#17](https://github.com/divmora/code-review-console/issues/17)


### Bug Fixes

* **dashboard:** show "–" for unstarted jobs and correct the footer ([#12](https://github.com/divmora/code-review-console/issues/12)) ([0466ee1](https://github.com/divmora/code-review-console/commit/0466ee131186a652e17c2fcb9e6a623f55b9b05a))
* **kubernetes:** own job secrets by patch and stop mounting the API token ([#19](https://github.com/divmora/code-review-console/issues/19)) ([4300973](https://github.com/divmora/code-review-console/commit/430097303ba52243d291b5bd211fe71f4e59468b)), closes [#14](https://github.com/divmora/code-review-console/issues/14) [#15](https://github.com/divmora/code-review-console/issues/15)

## [0.3.1](https://github.com/divmora/code-review-console/compare/v0.3.0...v0.3.1) (2026-11-19)


### Bug Fixes

* **license:** plans include their features ([b4529ed](https://github.com/divmora/code-review-console/commit/b4529ed2b3075e2aff88fb47bfab7618a2869854))

## [0.3.0](https://github.com/divmora/code-review-console/compare/v0.2.0...v0.3.0) (2026-11-19)


### Features

* **admin:** rebuild the admin panel with shadcn/ui and Tailwind ([f6c767b](https://github.com/divmora/code-review-console/commit/f6c767b0cd63dc706dbe5c188703d785eb4a75e0))


### Bug Fixes

* **release:** drop the Windows build ([dc94dca](https://github.com/divmora/code-review-console/commit/dc94dca2bb373e1b20b375b7621e134b28737bde))

## [0.2.0](https://github.com/divmora/code-review-console/compare/v0.1.0...v0.2.0) (2026-11-19)


### Features

* Divmora Code Review Console ([984db95](https://github.com/divmora/code-review-console/commit/984db9573742b28ebe54d23ec1b714dea1d99dae))
