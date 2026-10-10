ALTER TABLE repair_approvals DROP CONSTRAINT repair_approvals_phase_check;
ALTER TABLE repair_approvals ADD CONSTRAINT repair_approvals_phase_check
    CHECK (phase IN ('investigation', 'publication', 'automatic_publication'));
