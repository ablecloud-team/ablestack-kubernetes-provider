# Mold Kubernetes 구성 요소 검증 후보

기준: Apache Provider main 2a46b8e43382bbd1564db7a9bfa56f9caa872d13. 내부 HMAC-SHA256 및 저장소 네임스페이스를 유지하면서 원본 변경을 병합했습니다.

Origin Actions에서 계약/기능 테스트 후 amd64 바이너리, 커밋 고정 이미지, Go 모듈 목록, provenance와 docker archive를 생성합니다. 공식 배포와 Origin 후보를 구분하며 ISO는 후보의 실제 digest와 source SHA를 lock에 기록합니다.

Provider는 최신 SDK 생성 API와 VPC ACL, 소스 CIDR, Proxy Protocol, 사용자 지정 LB IP의 소유권 정리, providerID, 영역/region, 페이지네이션 및 프로토콜 변경 동작을 포함합니다. source CIDR 업데이트는 Mold backend의 cidrlist 구현이 필요합니다.

후보 SDK를 Origin Go 모듈로 replace합니다. SDK 공식 PR 병합/릴리즈 후 공식 모듈 버전으로 전환해야 합니다. 1.34 실제 LB/VPC 런타임과 arm64는 단위 테스트만으로 PASS 판정하지 않습니다.

## TCP LoadBalancer backend 준비 확인

Mold Provider는 새 TCP backend를 연결하기 전에 검증된 VM의 주 guest NIC 주소와 서비스 NodePort로 TCP 연결을 확인합니다. Node Ready가 먼저 표시되어도 CNI/kube-proxy의 데이터 경로가 아직 준비되지 않은 경우 기존 정상 backend를 보존하고 controller가 재시도합니다. 연결 하나는 최대2초, 한 번의 준비 확인은 최대5초로 제한하며 불필요해진 backend 제거는 계속 처리합니다. UDP에는 TCP 검사를 적용하지 않습니다.

Controller가 guest network 밖에 있거나 egress 정책상 NodePort에 접근할 수 없는 배치에서는 Service annotation `service.beta.kubernetes.io/cloudstack-load-balancer-backend-readiness-check: "false"`로 명시적으로 해제할 수 있습니다. 기본값은 true이며 잘못된 boolean은 오류로 거부합니다. 이 검사는 Pod 애플리케이션의 HTTP readiness 검사를 대체하지 않습니다.
