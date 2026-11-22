-- 0002: the Favourites Space, seeded the way 0001 seeds Read later.
--
-- Phase 5's import turns Raindrop's `favorite` column into membership of this
-- Space (spike S9): Autumn asked for favourites to be a real thing rather than a
-- dry-run statistic. The row is what lets an import file them, an export carry
-- them and phase 9's Space UI show them; the item-level star toggle is later
-- work, and needs no schema of its own because membership is the flag.
INSERT INTO spaces (id, slug, name, kind) VALUES ('favourites', 'favourites', 'Favourites', 'manual');
