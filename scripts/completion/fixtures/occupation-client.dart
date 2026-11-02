import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:planext4u_api_client/planext4u_api_client.dart';
import 'package:planext4u_experience/planext4u_experience.dart';
import 'package:planext4u_identity/planext4u_identity.dart';

final class LiveSession implements ApiAuthSession {
  LiveSession(this.token);
  final String token;
  @override
  Future<String?> accessToken() async => token;
  @override
  Future<bool> refresh() async => false;
}

void main() {
  test(
    'normal Flutter HTTP selects an admin-created occupation, signs in again and exports its safe reference',
    () async {
      final path = Platform.environment['PLANEXT_OCCUPATION_CONFIG'];
      if (path == null)
        throw StateError(
          'A uniquely owned local acceptance configuration is required.',
        );
      final config =
          jsonDecode(await File(path).readAsString()) as Map<String, dynamic>;
      ApiClient client(String token) => ApiClient(
        baseUrl: Uri.parse(config['base_url'] as String),
        transport: IoApiTransport(),
        authSession: LiveSession(token),
        retryBudget: const ApiRetryBudget(maxAttempts: 1),
      );
      final first = client(config['access_token'] as String);
      final occupations = ProfileOccupationApi(first, country: 'IN');
      final initial = await occupations.current();
      expect(initial.revision, 0);
      expect(initial.occupation, isNull);
      final page = await occupations.search(config['code'] as String);
      expect(
        page.items.map((item) => item.id),
        contains(config['occupation_id']),
      );
      final saved = await occupations.save(
        config['occupation_id'] as String,
        initial.revision,
      );
      expect(saved.revision, 1);
      expect(saved.occupation!.nameTA, 'ஆசிரியர்');
      final reauthenticated = (await first.send(
        ApiRequest(
          operation: 'identity.occupation_acceptance_sign_in',
          method: 'POST',
          path: '/v1/auth/exchange',
          authRequired: false,
          replayPolicy: ApiReplayPolicy.never,
          body: {
            'provider': 'local',
            'provider_token': config['provider_token'],
            'device_id': config['second_device'],
            'country': 'IN',
          },
        ),
        IdentityAuthentication.fromJson,
      )).value;
      final second = client(reauthenticated.tokens.accessToken);
      try {
        final persisted = await ProfileOccupationApi(
          second,
          country: 'IN',
        ).current();
        expect(persisted.revision, 1);
        expect(persisted.occupation!.id, saved.occupation!.id);
        final exported = await Phase5Api(second).exportAccountData();
        final document =
            jsonDecode(exported.document!.json) as Map<String, dynamic>;
        final rows = document['occupation_references'] as List;
        expect(rows, hasLength(1));
        final reference = (rows.single as Map)['occupation'] as Map;
        expect(reference['id'], saved.occupation!.id);
        expect(reference['master_revision'], 1);
        expect(reference.containsKey('provider_subject'), isFalse);
        expect(reference.containsKey('refresh_token'), isFalse);
      } finally {
        await second.send(
          ApiRequest(
            operation: 'identity.occupation_acceptance_logout',
            method: 'POST',
            path: '/v1/auth/revoke',
            authRequired: false,
            replayPolicy: ApiReplayPolicy.never,
            body: {'refresh_token': reauthenticated.tokens.refreshToken},
          ),
          (value) => value,
        );
      }
    },
  );
}
