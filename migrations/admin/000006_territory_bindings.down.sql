SET ROLE planext4u_admin_owner;
DROP FUNCTION admin.fulfillment_territory_allowed(uuid,char,uuid,uuid,text);
DROP TABLE admin.fulfillment_territory_binding_events;
DROP TABLE admin.fulfillment_territory_bindings;
RESET ROLE;
