SET ROLE planext4u_identity_owner;
DROP FUNCTION identity.privacy_supply_record_retained(text,text,text);
DROP FUNCTION identity.privacy_fulfillment_record_retained(text,text,text);
DROP FUNCTION identity.privacy_supply_erasure_scope(text);
DROP FUNCTION identity.privacy_fulfillment_erasure_scope(text);
DROP FUNCTION identity.privacy_record_retained(text,text,text,text);
DROP FUNCTION identity.privacy_owner_erasure_scope(text,text);
RESET ROLE;
