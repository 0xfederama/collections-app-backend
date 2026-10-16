-- LOSSY by design: from API 0.7.0 on, generic, movie and tv_show items have no price, and the price columns,
-- pinned `price` keys and price sort modes of their lists were cleared by the up migration. The old prices
-- stay in the Markdown "From 1.3" blocks of their items. Take a backup first (BACKUP_AND_DEPLOYMENT §4).
SELECT 1;
