-- Receipt recovery follows dispatch order even if wall-clock timestamps move
-- backwards or round to the same database precision.
ALTER TABLE repair_tool_receipts
    ADD COLUMN receipt_order BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE;
