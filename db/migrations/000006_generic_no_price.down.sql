-- LOSSY by design: generic lists have no price from API 0.6.0 on, and the price columns, pinned `price` keys and
-- price sort modes of generic lists were cleared by the up migration. The old prices stay in the Markdown
-- "From 1.2" blocks of their items. Take a backup first (BACKUP_AND_DEPLOYMENT §4).
SELECT 1;
