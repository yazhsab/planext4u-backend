\set ON_ERROR_STOP on

INSERT INTO identity.identities (id,tenant_id,provider,provider_subject,created_at)
VALUES ('2da29782-f277-40ba-a526-d153be421243','afc1e0db-73cf-40b3-9927-33590133da0b','local','local-customer-001',now())
ON CONFLICT (id) DO NOTHING;
INSERT INTO identity.profiles (identity_id,display_name,email,phone,locale,time_zone,version,updated_at)
VALUES ('2da29782-f277-40ba-a526-d153be421243','Local Customer','customer@example.test','+919999999999','en','Asia/Kolkata',1,now())
ON CONFLICT (identity_id) DO UPDATE SET email=excluded.email,phone=excluded.phone,updated_at=excluded.updated_at;
INSERT INTO identity.identity_roles (identity_id,role,granted_at)
VALUES ('2da29782-f277-40ba-a526-d153be421243','CUSTOMER',now())
ON CONFLICT DO NOTHING;

INSERT INTO social.policies
    (tenant_id,country,version,ranking_model,review_terms,story_ttl_seconds,reel_ttl_seconds,media_retention_seconds,presence_ttl_seconds,call_ttl_seconds,like_reward_points,follow_reward_points,share_reward_points,reward_expiry_seconds,updated_at)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','social-local-v1','socio-feed-v1',ARRAY['manual-review'],86400,2592000,2592000,120,120,10,20,15,31536000,now())
ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,ranking_model=excluded.ranking_model,review_terms=excluded.review_terms,like_reward_points=excluded.like_reward_points,follow_reward_points=excluded.follow_reward_points,share_reward_points=excluded.share_reward_points,updated_at=now();
INSERT INTO social.profiles
    (identity_id,tenant_id,country,handle,display_name,bio,private,verified,created_at,updated_at)
VALUES
    ('2da29782-f277-40ba-a526-d153be421243','afc1e0db-73cf-40b3-9927-33590133da0b','IN','local_customer','Local Customer','P4U local validation customer',false,true,now(),now()),
    ('529a925f-2a7f-433e-b038-0007e13b0ab8','afc1e0db-73cf-40b3-9927-33590133da0b','IN','local_neighbour','Local Neighbour','Verified neighbourhood profile',false,true,now(),now()),
    ('1905509c-b808-4515-8a1d-d5e617bba8bf','afc1e0db-73cf-40b3-9927-33590133da0b','IN','local_moderator','Local Moderator','Synthetic moderation identity',false,true,now(),now()),
    ('00000000-0000-4000-8000-000000000003','afc1e0db-73cf-40b3-9927-33590133da0b','IN','retention_worker','Retention Worker','System retention identity',false,true,now(),now())
ON CONFLICT (identity_id) DO UPDATE SET handle=excluded.handle,display_name=excluded.display_name,bio=excluded.bio,verified=excluded.verified,updated_at=now();
INSERT INTO social.posts
    (id,tenant_id,country,author_identity_id,revision,body,hashtags,mentions,status,ranking_version,created_at,updated_at)
VALUES
    ('0bc5faaa-a8cb-4531-b931-708847dd0d60','afc1e0db-73cf-40b3-9927-33590133da0b','IN','529a925f-2a7f-433e-b038-0007e13b0ab8',1,'Welcome to the durable P4U neighbourhood feed #local',ARRAY['local'],'{}','PUBLISHED','socio-feed-v1',now(),now())
ON CONFLICT (id) DO UPDATE SET status='PUBLISHED',ranking_version=excluded.ranking_version,updated_at=now();

INSERT INTO governance.country_controls
    (tenant_id,country,currency,locales,feature_flags,policy_version,revision,published_at,published_by_identity_id)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','INR',ARRAY['en','ta'],'{"socio":true,"shop":true,"services":true,"homes":true,"classifieds":true,"emergency":true,"franchise":true}'::jsonb,'governance-local-v1',1,now(),'1905509c-b808-4515-8a1d-d5e617bba8bf')
ON CONFLICT (tenant_id,country) DO UPDATE SET feature_flags=excluded.feature_flags,policy_version=excluded.policy_version,revision=governance.country_controls.revision+1,published_at=now(),published_by_identity_id=excluded.published_by_identity_id;
INSERT INTO governance.report_cards
    (tenant_id,country,id,title,domain,metric,value,unit,freshness,published)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','orders-completed','Orders','commerce','completed',1234,'count',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','revenue-net','Revenue','finance','net',884400,'minor_currency',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','wallet-liability','Wallet','wallet','liability',45000,'points',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','vendors-approved','Vendors','supply','approved',41,'count',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','riders-online','Riders','fulfillment','online',17,'count',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','social-active','Socio active','social','active',5600,'count',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','homes-active','Homes active','homes','active',98,'count',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','classifieds-active','Classifieds active','classifieds','active',340,'count',now(),true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','emergency-sla','Emergency within SLA','emergency','within_sla',99,'percent',now(),true)
ON CONFLICT (tenant_id,country,id) DO UPDATE SET value=excluded.value,freshness=now(),published=true;
INSERT INTO governance.map_cells (tenant_id,country,region_code,label,count,intensity,published)
VALUES ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','IN-TN-CHN','Chennai',281,82,true)
ON CONFLICT (tenant_id,country,region_code) DO UPDATE SET count=excluded.count,intensity=excluded.intensity,published=true;
INSERT INTO governance.leaderboard_entries (tenant_id,country,board,rank,masked_label,score,badge,published)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','community',1,'Loc***ide',980,'Community guide',true),
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','community',2,'Gre***art',920,'Trusted seller',true)
ON CONFLICT (tenant_id,country,board,rank) DO UPDATE SET masked_label=excluded.masked_label,score=excluded.score,badge=excluded.badge,published=true;
INSERT INTO governance.insights (tenant_id,country,id,title,summary,confidence,evidence,generated_at,published)
VALUES ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','evening-demand','Evening demand','Local-service demand peaks between 18:00 and 20:00.','HIGH',ARRAY['30-day bookings','minimum cohort 100'],now(),true)
ON CONFLICT (tenant_id,country,id) DO UPDATE SET summary=excluded.summary,confidence=excluded.confidence,evidence=excluded.evidence,generated_at=now(),published=true;

INSERT INTO wallet.accounts (tenant_id,country,customer_identity_id,balance_points,revision,updated_at)
VALUES ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','2da29782-f277-40ba-a526-d153be421243',100000,1,now())
ON CONFLICT (tenant_id,country,customer_identity_id) DO UPDATE
SET balance_points=GREATEST(wallet.accounts.balance_points,100000),revision=wallet.accounts.revision+1,updated_at=now();
INSERT INTO wallet.ledger_entries
    (id,tenant_id,country,customer_identity_id,entry_type,category,source_reference,delta_points,balance_after,original_expiry_at,idempotency_key,request_fingerprint,created_at)
VALUES
    ('6be6b4c2-ab2b-44f6-8098-d1a53db48179','afc1e0db-73cf-40b3-9927-33590133da0b','IN','2da29782-f277-40ba-a526-d153be421243',
     'CREDIT','LOCAL_SEED','local-production-validation',100000,100000,now()+interval '365 days','local-wallet-seed-000001',repeat('0',64),now())
ON CONFLICT (id) DO NOTHING;
INSERT INTO wallet.credit_lots (ledger_entry_id,remaining_points,expires_at)
VALUES ('6be6b4c2-ab2b-44f6-8098-d1a53db48179',100000,now()+interval '365 days')
ON CONFLICT (ledger_entry_id) DO UPDATE SET remaining_points=GREATEST(wallet.credit_lots.remaining_points,100000),expires_at=excluded.expires_at;

INSERT INTO catalog.items (id,tenant_id,country,kind,title,status,priority,version,published_at,updated_at)
VALUES (
    '45f713f6-a2d2-4a55-84bd-29f4f2afe3ba','afc1e0db-73cf-40b3-9927-33590133da0b','IN','ITEM',
    jsonb_build_object(
        'id','45f713f6-a2d2-4a55-84bd-29f4f2afe3ba',
        'vendor_id','d5c667ab-3019-48f3-be65-bd742fa7bed2',
        'category_id','5ad56f8d-47ad-454c-9e76-cac617cf47fe',
        'name','Local production validation item',
        'summary','Durable checkout validation item',
        'price',jsonb_build_object('amount_minor',12500,'currency','INR'),
        'available',true,
        'seller_name','Local Verified Vendor',
        'verified_local_seller',true,
        'variants',jsonb_build_array(jsonb_build_object(
            'id','74839a1c-64f0-45fc-8afa-e9466ea4bdb3','label','Standard',
            'price',jsonb_build_object('amount_minor',12500,'currency','INR'),
            'available',true,'stock_quantity',100,'max_per_order',10
        ))
    ),
    'PUBLISHED',10,1,now(),now()
)
ON CONFLICT (id) DO UPDATE SET title=excluded.title,status='PUBLISHED',published_at=now(),updated_at=now();

INSERT INTO inventory.stock (tenant_id,country,variant_id,available_quantity,revision,updated_at)
VALUES ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','74839a1c-64f0-45fc-8afa-e9466ea4bdb3',100,1,now())
ON CONFLICT (tenant_id,country,variant_id) DO UPDATE SET available_quantity=100,revision=inventory.stock.revision+1,updated_at=now();

UPDATE commerce.pricing_policies SET active=false
WHERE tenant_id='afc1e0db-73cf-40b3-9927-33590133da0b' AND country='IN' AND active;
INSERT INTO commerce.pricing_policies
    (id,tenant_id,country,version,product_tax_basis_points,product_tax_treatment,platform_fee_minor,platform_fee_tax_basis_points,wallet_point_value_minor,wallet_mode,quote_ttl_seconds,reservation_ttl_seconds,active,published_at,published_by)
VALUES
    ('e262b755-68ef-4c0f-a448-896579bfe8de','afc1e0db-73cf-40b3-9927-33590133da0b','IN','pricing-local-v1',500,'INCLUSIVE',500,1800,1,'HYBRID_PAYMENT',600,600,true,now(),'1905509c-b808-4515-8a1d-d5e617bba8bf')
ON CONFLICT (id) DO UPDATE SET active=true,published_at=now();

INSERT INTO commerce.delivery_slots (id,tenant_id,country,window_start,window_end,fee_minor,currency,capacity,revision)
VALUES ('242839bc-3e86-4c06-987e-2a780fcac895','afc1e0db-73cf-40b3-9927-33590133da0b','IN',now()+interval '1 hour',now()+interval '3 hours',2500,'INR',100,1)
ON CONFLICT (id) DO UPDATE SET window_start=excluded.window_start,window_end=excluded.window_end,capacity=100,revision=commerce.delivery_slots.revision+1;

INSERT INTO commerce.promotions (id,tenant_id,country,code,minimum_subtotal_minor,discount_basis_points,maximum_discount_minor,starts_at,ends_at,active)
VALUES ('90e34ec2-8ef5-41e3-a395-9fc12be0b27b','afc1e0db-73cf-40b3-9927-33590133da0b','IN','SAVE10',10000,1000,5000,now()-interval '1 day',now()+interval '30 days',true)
ON CONFLICT (id) DO UPDATE SET starts_at=excluded.starts_at,ends_at=excluded.ends_at,active=true;

INSERT INTO commerce.postal_zones (id,tenant_id,country,postal_code,locality,active,revision,updated_at,updated_by)
VALUES ('94236626-689a-44b0-894c-0fbfc545b625','afc1e0db-73cf-40b3-9927-33590133da0b','IN','600001','Chennai',true,1,now(),'1905509c-b808-4515-8a1d-d5e617bba8bf')
ON CONFLICT (id) DO UPDATE SET active=true,revision=commerce.postal_zones.revision+1,updated_at=now();

UPDATE commerce.commercial_policies SET active=false
WHERE tenant_id='afc1e0db-73cf-40b3-9927-33590133da0b' AND country='IN' AND active;
INSERT INTO commerce.commercial_policies
    (id,tenant_id,country,version,default_vendor_tier,default_commission_basis_points,default_wallet_basis_points,active,published_at,published_by)
VALUES
    ('28cfb09e-2276-4144-87c3-b746a128be4b','afc1e0db-73cf-40b3-9927-33590133da0b','IN','commercial-local-v1','BASIC',1000,2000,true,now(),'1905509c-b808-4515-8a1d-d5e617bba8bf')
ON CONFLICT (id) DO UPDATE SET active=true,published_at=now();
INSERT INTO commerce.commercial_product_rules
    (id,policy_id,vendor_id,item_id,variant_id,vendor_tier,commission_basis_points,wallet_basis_points)
VALUES
    ('0e11755a-b914-46f8-9302-cf7873be5acf','28cfb09e-2276-4144-87c3-b746a128be4b','d5c667ab-3019-48f3-be65-bd742fa7bed2','45f713f6-a2d2-4a55-84bd-29f4f2afe3ba','74839a1c-64f0-45fc-8afa-e9466ea4bdb3','PREMIUM',750,3000)
ON CONFLICT (id) DO UPDATE SET commission_basis_points=excluded.commission_basis_points,wallet_basis_points=excluded.wallet_basis_points;

INSERT INTO booking.offerings
    (id,tenant_id,country,provider_id,category_id,name,summary,duration_minutes,price_minor,advance_minor,currency,payment_mode,
     cancellation_policy_ref,reschedule_policy_ref,active,revision,created_at,updated_at,provider_name,verified_provider,rating_average,completed_bookings,live_engagements)
VALUES
    ('4cd36ca5-ceb0-44d4-8b82-a6a70b33e8d6','afc1e0db-73cf-40b3-9927-33590133da0b','IN','ece509ab-a3fe-4ffd-a3bf-06d81b8ead38',
     'bd83b1cb-dce0-4e75-8658-216f5bb0a0e6','Home electrical repair','Verified local electrician',60,50000,10000,'INR','FULL',
     'cancel-v1','reschedule-v1',true,1,now(),now(),'P4U Verified Services',true,4.80,120,3)
ON CONFLICT (id) DO UPDATE SET active=true,updated_at=now(),provider_name=excluded.provider_name,verified_provider=true,rating_average=excluded.rating_average;

INSERT INTO booking.service_zones (offering_id,postal_code)
VALUES ('4cd36ca5-ceb0-44d4-8b82-a6a70b33e8d6','600001')
ON CONFLICT DO NOTHING;

INSERT INTO booking.slots
    (id,offering_id,provider_id,starts_at,ends_at,timezone,capacity,buffer_minutes,policy_version,provider_revision)
VALUES
    ('e171556b-b9bc-45f4-8984-38c904ea249e','4cd36ca5-ceb0-44d4-8b82-a6a70b33e8d6','ece509ab-a3fe-4ffd-a3bf-06d81b8ead38',now()+interval '1 day',now()+interval '1 day 1 hour','Asia/Kolkata',4,15,'policy-v1',1),
    ('12276a34-a8a9-4f24-81df-380eaf8a7bb6','4cd36ca5-ceb0-44d4-8b82-a6a70b33e8d6','ece509ab-a3fe-4ffd-a3bf-06d81b8ead38',now()+interval '2 days',now()+interval '2 days 1 hour','Asia/Kolkata',4,15,'policy-v1',1)
ON CONFLICT (id) DO UPDATE SET starts_at=excluded.starts_at,ends_at=excluded.ends_at,capacity=excluded.capacity,provider_revision=booking.slots.provider_revision+1;

INSERT INTO booking.policies
    (tenant_id,country,version,hold_ttl_seconds,cancellation_cutoff_seconds,maximum_free_reschedules,start_otp_validity_seconds,completion_confirm_seconds,wallet_point_value_minor,updated_at)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','policy-v1',600,3600,2,86400,172800,100,now())
ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,hold_ttl_seconds=excluded.hold_ttl_seconds,updated_at=now();

INSERT INTO food.restaurants
    (id,tenant_id,country,owner_identity_id,name,cuisine,postal_codes,rating,verified,open,accept_until_minute,preparation_minutes,delivery_fee_minor,minimum_order_minor,currency,updated_at)
VALUES
    ('fbb9e354-e5ef-45f6-9409-48aa93143c6e','afc1e0db-73cf-40b3-9927-33590133da0b','IN','0c556fe6-f236-4d1c-bda8-afec410b4c07',
     'P4U Local Kitchen',ARRAY['South Indian','Healthy'],ARRAY['600001'],4.80,true,true,1440,25,2000,10000,'INR',now())
ON CONFLICT (id) DO UPDATE SET verified=true,open=true,updated_at=now();
INSERT INTO food.menu_items
    (id,restaurant_id,name,description,category,vegetarian,base_price_minor,currency,available,image_asset_id)
VALUES
    ('9991e88a-214d-4a94-92cd-6a86c2b1fbb8','fbb9e354-e5ef-45f6-9409-48aa93143c6e','Millet meal','Healthy local millet meal','Meals',true,10000,'INR',true,'4c6dc348-1753-497f-8b19-f2404db47ab3')
ON CONFLICT (id) DO UPDATE SET available=true,base_price_minor=excluded.base_price_minor;
INSERT INTO food.option_groups (id,menu_item_id,name,minimum,maximum,instructions)
VALUES ('7db4fc10-b632-4c98-812d-901f1ca72741','9991e88a-214d-4a94-92cd-6a86c2b1fbb8','Portion',1,1,'Choose one portion')
ON CONFLICT (id) DO UPDATE SET minimum=1,maximum=1,instructions=excluded.instructions;
INSERT INTO food.options (id,option_group_id,name,price_delta_minor,currency,available)
VALUES ('632352bc-1db5-450b-84d5-adf057fe7aaa','7db4fc10-b632-4c98-812d-901f1ca72741','Large',2000,'INR',true)
ON CONFLICT (id) DO UPDATE SET price_delta_minor=excluded.price_delta_minor,available=true;
INSERT INTO food.policies (tenant_id,country,version,cart_ttl_seconds,acceptance_ttl_seconds,tax_basis_points,wallet_point_value_minor,updated_at)
VALUES ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','food-pricing-v1',1200,180,500,100,now())
ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,cart_ttl_seconds=excluded.cart_ttl_seconds,acceptance_ttl_seconds=excluded.acceptance_ttl_seconds,tax_basis_points=excluded.tax_basis_points,wallet_point_value_minor=excluded.wallet_point_value_minor,updated_at=now();

INSERT INTO fulfillment.policies
    (tenant_id,country,version,offer_ttl_seconds,location_ttl_seconds,chat_after_delivery_seconds,settlement_cooling_seconds,commission_basis_points,tax_basis_points,updated_at)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','fulfillment-local-v1',300,120,7200,0,500,500,now())
ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,offer_ttl_seconds=excluded.offer_ttl_seconds,location_ttl_seconds=excluded.location_ttl_seconds,chat_after_delivery_seconds=excluded.chat_after_delivery_seconds,settlement_cooling_seconds=excluded.settlement_cooling_seconds,commission_basis_points=excluded.commission_basis_points,tax_basis_points=excluded.tax_basis_points,updated_at=now();

INSERT INTO fulfillment.territories
    (id,tenant_id,country,region_id,franchise_identity_id,name,center_latitude,center_longitude,radius_km,postal_codes)
VALUES
    ('47ef5141-ddc8-4bd2-b918-d75d6717a43d','afc1e0db-73cf-40b3-9927-33590133da0b','IN','CHENNAI','1905509c-b808-4515-8a1d-d5e617bba8bf','Chennai Central',13.0827,80.2707,5,ARRAY['600001'])
ON CONFLICT (id) DO UPDATE SET region_id=excluded.region_id,name=excluded.name,center_latitude=excluded.center_latitude,center_longitude=excluded.center_longitude,radius_km=excluded.radius_km,postal_codes=excluded.postal_codes;

INSERT INTO emergency.policies
    (tenant_id,country,version,assignment_sla_seconds,location_max_age_seconds,updated_at)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','emergency-local-v1',300,120,now())
ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,assignment_sla_seconds=excluded.assignment_sla_seconds,location_max_age_seconds=excluded.location_max_age_seconds,updated_at=now();

INSERT INTO local_verticals.policies
    (tenant_id,country,version,currency,estimator_version,review_terms,classified_lifetime_seconds,feature_lifetime_seconds,report_review_threshold,updated_at)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','marketplace-local-v1','INR','homes-avm-local-v1',ARRAY['manual-review'],2592000,604800,3,now())
ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,currency=excluded.currency,estimator_version=excluded.estimator_version,review_terms=excluded.review_terms,classified_lifetime_seconds=excluded.classified_lifetime_seconds,feature_lifetime_seconds=excluded.feature_lifetime_seconds,report_review_threshold=excluded.report_review_threshold,updated_at=now();

INSERT INTO local_verticals.owner_verifications
    (tenant_id,country,owner_identity_id,status,evidence_reference,verified_by_identity_id,verified_at)
VALUES
    ('afc1e0db-73cf-40b3-9927-33590133da0b','IN','d449d17b-dc90-4efa-abde-0e2a70aac0ac','VERIFIED','local-kyc-evidence-001','1905509c-b808-4515-8a1d-d5e617bba8bf',now())
ON CONFLICT (tenant_id,country,owner_identity_id) DO UPDATE SET status='VERIFIED',evidence_reference=excluded.evidence_reference,verified_by_identity_id=excluded.verified_by_identity_id,verified_at=now();
