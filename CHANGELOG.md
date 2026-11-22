# Changelog

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
