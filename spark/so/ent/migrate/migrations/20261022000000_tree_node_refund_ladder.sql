-- Modify "tree_nodes" table
ALTER TABLE "tree_nodes" ADD COLUMN IF NOT EXISTS "raw_refund_ladder" bytea NULL;
