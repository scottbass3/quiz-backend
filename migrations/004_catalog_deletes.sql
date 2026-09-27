-- Migration: 004_catalog_deletes
-- Question lists can now be deleted. Their questions and custom themes are
-- removed by existing ON DELETE CASCADE constraints; games that were played
-- with the list keep their history and lose the reference.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'games_question_list_id_fkey' AND confdeltype <> 'n'  -- 'n' = SET NULL
    ) THEN
        ALTER TABLE games DROP CONSTRAINT games_question_list_id_fkey;
        ALTER TABLE games ADD CONSTRAINT games_question_list_id_fkey
            FOREIGN KEY (question_list_id) REFERENCES question_lists(id) ON DELETE SET NULL;
    END IF;
END $$;
