import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:planext4u_api_client/planext4u_api_client.dart';
import 'package:planext4u_experience/planext4u_experience.dart';
import 'package:planext4u_identity/planext4u_identity.dart';

final class GeographyLiveSession implements ApiAuthSession {
  GeographyLiveSession(this.token);
  final String token;
  @override
  Future<String?> accessToken() async => token;
  @override
  Future<bool> refresh() async => false;
}

final class GeographyObservedTransport implements ApiTransport {
  final delegate = IoApiTransport();
  final paths = <String>[];
  @override
  Future<TransportResponse> send(
    TransportRequest request, {
    required Duration timeout,
    required ApiCancellationToken cancellationToken,
  }) {
    paths.add(request.url.path);
    return delegate.send(
      request,
      timeout: timeout,
      cancellationToken: cancellationToken,
    );
  }
}

void main() {
  test(
    'normal Flutter geography selection and address readback work without checkout or wallet requests',
    () async {
      final configPath = Platform.environment['PLANEXT_GEOGRAPHY_CONFIG'];
      if (configPath == null) {
        throw StateError(
          'Owned local geography acceptance configuration required.',
        );
      }
      final config =
          jsonDecode(await File(configPath).readAsString())
              as Map<String, dynamic>;
      final transport = GeographyObservedTransport();
      ApiClient client(String token) => ApiClient(
        baseUrl: Uri.parse(config['base_url'] as String),
        transport: transport,
        authSession: GeographyLiveSession(token),
        retryBudget: const ApiRetryBudget(maxAttempts: 1),
      );
      final first = client(config['access_token'] as String);
      final directory = GeographyApi(first, country: 'IN');
      final picker = GeographyController(directory, country: 'IN');
      final expected = (config['path'] as List).cast<Map<String, dynamic>>();
      for (var i = 0; i < 5; i++) {
        await picker.load(query: expected[i]['code'] as String);
        expect(picker.status, GeographyStatus.ready);
        final selected = picker.page!.items.singleWhere(
          (e) => e.id == expected[i]['id'],
        );
        expect(await picker.choose(selected), isTrue);
      }
      final selection = picker.selection!;
      expect(selection.areaID, expected.last['id']);
      final controller = TransactionController(
        remote: TransactionApi(first),
        geography: directory,
        addressCountry: 'IN',
      );
      try {
        await controller.loadAddresses();
        expect(controller.state.status, TransactionStatus.ready);
        expect(controller.state.addresses, isEmpty);
        expect(
          await controller.saveAddress(
            draft: CustomerAddressDraft(
              label: 'வீடு',
              line1: '123 Local Acceptance Road',
              postalCode: '600001',
              locality: 'Chennai',
              isDefault: true,
              geographyAreaID: selection.areaID,
            ),
          ),
          isTrue,
        );
        final saved = controller.state.addresses.single;
        expect(saved.geography!.areaID, selection.areaID);
        expect(
          saved.geography!.path.map((e) => e.id).toList(),
          selection.path.map((e) => e.id).toList(),
        );
        expect(saved.revision, 1);
        expect(saved.isDefault, isTrue);
        final signedIn = (await first.send(
          ApiRequest(
            operation: 'identity.geography_acceptance_sign_in',
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
        final second = client(signedIn.tokens.accessToken),
            reopened = TransactionController(
              remote: TransactionApi(client(signedIn.tokens.accessToken)),
              addressCountry: 'IN',
            );
        try {
          await reopened.loadAddresses();
          expect(reopened.state.status, TransactionStatus.ready);
          final persisted = reopened.state.addresses.single;
          expect(persisted.id, saved.id);
          expect(persisted.geography!.areaID, saved.geography!.areaID);
          expect(persisted.geography!.path.last.masterRevision, 1);
        } finally {
          reopened.dispose();
          await second.send(
            ApiRequest(
              operation: 'identity.geography_acceptance_logout',
              method: 'POST',
              path: '/v1/auth/revoke',
              authRequired: false,
              replayPolicy: ApiReplayPolicy.never,
              body: {'refresh_token': signedIn.tokens.refreshToken},
            ),
            (value) => value,
          );
        }
        expect(
          transport.paths.where(
            (path) =>
                path.startsWith('/v1/wallet') || path == '/v1/delivery-slots',
          ),
          isEmpty,
        );
      } finally {
        controller.dispose();
        picker.dispose();
      }
    },
  );
}
