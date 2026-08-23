plugins {
    `java-library`
}

group = "io.klaro"
version = "0.1.0"

java {
    withSourcesJar()
}

repositories {
    mavenCentral()
}

val otelVersion = "1.65.0"
val springBootVersion = "3.5.16"
val junitVersion = "5.14.4"

dependencies {
    api(platform("io.opentelemetry:opentelemetry-bom:$otelVersion"))
    api("io.opentelemetry:opentelemetry-api")
    api("io.opentelemetry:opentelemetry-sdk")
    api("io.opentelemetry:opentelemetry-exporter-otlp")

    // @Nullable 애노테이션(컴파일 전용 - 런타임에는 필요 없다).
    compileOnly("com.google.code.findbugs:jsr305:3.0.2")
    testCompileOnly("com.google.code.findbugs:jsr305:3.0.2")

    // Spring Boot 헬퍼(io.klaro.apm.spring)를 컴파일하는 데만 필요 - 최종 사용자가 Spring Boot를
    // 쓰지 않으면 이 의존성 없이도 klaro-apm 코어를 사용할 수 있다(SDK_CONTRACT.md 7 "불필요한
    // 의존성 강제 설치 방지" 원칙, extras/optional peer dependency의 Java식 대응).
    compileOnly(platform("org.springframework.boot:spring-boot-dependencies:$springBootVersion"))
    compileOnly("org.springframework.boot:spring-boot")

    testImplementation(platform("io.opentelemetry:opentelemetry-bom:$otelVersion"))
    testImplementation("io.opentelemetry:opentelemetry-sdk-testing")
    testImplementation(platform("org.springframework.boot:spring-boot-dependencies:$springBootVersion"))
    testImplementation("org.springframework.boot:spring-boot")
    testImplementation("org.junit.jupiter:junit-jupiter:$junitVersion")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.withType<JavaCompile> {
    options.encoding = "UTF-8"
    options.release.set(17)
}

tasks.test {
    useJUnitPlatform()
}
