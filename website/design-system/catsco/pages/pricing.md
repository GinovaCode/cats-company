# CatsCo Pricing Page Override

Updated: 2026-10-29. This revision supersedes the Free / Pro ¥399 / Max ¥799 layout for this page.

## Source and scope

Reproduce the current platform chooser rather than introduce a marketing design. Read-only reference: `E:/CodexProjects/catsco-plans-pr-20261027/webapp/src/widgets/plans/`, including `catsco-plans-modal.jsx`, `plan-offers.js`, `plan-chooser.css`, `enterprise-team.*`, and `enterprise-services.*`. `webapp/src/dev/README.md` and the default E layout in `plans-preview.jsx` establish the current composition. PR #805 is reference only.

Pricing implementation migrated from the user-confirmed D:/CatsCo官网新版整合 preview into the current remote website/ project. Preserve current remote shared components, lazy route loading and authentication links. Keep its current header, footer, removed enterprise route, link destinations and heading tokens. Preserve the pricing-owned header styles verbatim. The chooser is page content, so omit the platform's close-page control, account usage and order history. Preserve the substantial invitation card, grouped plan comparison and FAQ below the chooser; update their content to match these proposals. No platform files or services are changed.

## Visual contract

- Heading: 选择适合你的套餐. Centered segmented tabs: 个人 / 团队 / 企业, following the latest user-supplied screenshots while retaining the graphite dark palette.
- Use the platform dark variant within the latest graphite website: #171717 canvas, #252525 cards, #ECECEC text and inverse actions, #B8B8BE secondary text, #A2A2A9 muted text, #454549 borders. Keep global CatsCo green #1A9D7A and #11745B for site chrome, focus and invitation actions.
- Three equal cards; 16px radius, 20px gap, 24px vertical padding, 28px names, 50px prices. Match platform content tracks with subgrid so actions, quotas, feature starts and footnotes align across cards. No colored top edges or invented recommendation badge.
- Use the existing Inter / Noto Sans SC stack. Keep the platform's quiet hover tint with no movement.
- Team tab: a standalone 40/60 team offer, ¥599/month, at most 10 members and up to 30 亿 Token; four benefits cover the dedicated server, team experience and Skill, shared knowledge and permissions.
- Enterprise tab: only the 40/60 Business Start offer, ¥4,999/month and up, with the neutral embossed scene and five-item service workspace. Keep advanced services separately scoped. The white references define composition and content only; preserve the current dark mode and inverse light actions.
- Retain the screenshot's EARLY ACCESS invitation card: a substantial graphite surface, green eyebrow and login action, without the file-icon preview notice below it, per the latest annotated feedback. Use dark button text on green for contrast.
- Retain PLAN COMPARISON / 比较不同方案 with four package columns: 基础版、个人版、专业版、团队版. Group rows into 方案与额度、日常工作与持续执行、团队协作与资源. All group bars use neutral gray (#292929 background, #CECECE text), without a color tint. The comparison header contains only the four plan names; omit choose-plan buttons per the latest annotation. Read prices and quotas from shared package data. Team receives only source-confirmed capabilities; its other personal/pro capabilities remain explicitly 待确认. Do not assume inheritance.
- Use a semantic table, row and group headers, accessible check/dash labels, and a labeled keyboard-focusable horizontal scroll region on narrow screens. Keep the feature-name column visible while scrolling; never cause page-level horizontal overflow. Package actions remain in the offer cards above the comparison table.
- Retain the raised graphite FAQ surface, regular-weight questions, fixed question-row rhythm, individual toggles and expand/collapse all.
- Follow the website's 1200px maximum content width. Below 821px, stack the cards within 480px; mobile has 20px gutters. Primary controls meet the website's 48px minimum.

## Proposal content, not live sales

| Proposal | Price per 30 days | Reference quota |
| --- | --- | --- |
| 基础版 / CATSCO BASIC | ¥59 | 5 亿 Token |
| 个人版 / CATSCO PERSONAL | ¥99 | 约 10 亿 Token |
| 专业版 / CATSCO PRO | ¥159 | 约 20 亿 Token |
| 团队版 / CATSCO TEAM | ¥599 | 最高 30 亿 Token |

Preserve the source feature ordering and nested Office / work-preference / parallel-task details. Replace Agent in public feature wording with the user-facing task description. To follow the explicit request to reproduce package contents, retain Token only as the source's quota unit and explain that it is estimated; avoid internal routing, cache and implementation vocabulary.

Team: at most 10 people; dedicated 8-core CPU / 16 GB server; shared experience and Skill, knowledge base and permissions. Personal and professional proposals describe a cloud AI employee valid with the plan and a 15-day post-expiry retention period. These are draft terms awaiting owner approval, including quota measurement, model-dependent actual usage and priority availability.

Business Start remains ¥4,999/month and up for scoped implementation service, not a team account fee. Keep all five source services and the separately scoped advanced-service limitation. Do not invent certifications or availability.

## Interaction and destinations

- Keep all three tab panels in the DOM with hidden state, tab/tabpanel relationships, roving tab index and ArrowLeft/ArrowRight/Home/End keyboard control.
- Every package action opens a local native dialog with the selected name, price, 30-day validity, quota and team size where applicable. State that buying is unavailable and disable the purchase control. Native dialog supplies modal focus containment, Escape dismissal and trigger focus restoration; lock background scrolling only while open.
- Do not create orders, invoke payments, modify account rights or request credentials.
- Do not pass new plans to the old login plan parser, which still describes the prior commercial proposal. Updating that separate page is outside this task.
- Invitation: use the existing `appLoginUrl({ access: 'invite', source: 'pricing', next: '/?open=relay' })` helper to keep authentication on the product domain.
- Enterprise consultation: `/contact?topic=enterprise&service=business-start&source=pricing`.
- Omit the preview/unavailable label above cards and the release notice below the invitation card, per the latest annotated feedback. Keep the existing draft terms in the quota note, comparison note, FAQ and preview dialog. Keep Business Start separate from the team subscription in the comparison footnote. Opening date and final prices, quota, retention, server resources and service scope require owner confirmation.
- Update only the pricing route description in the integration-owned routes file, plus affected existing contract assertions. Preserve all unrelated edits.

## Verification

Run pnpm test, pnpm run typecheck, pnpm run build and git diff --check. Review personal/team/enterprise and confirmation/FAQ states at 390, 768, 1024 and 1440 widths using a dedicated strict port, never shared 5175. Do not commit, push or publish.
