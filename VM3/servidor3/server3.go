package main

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	pb "servidor3/proto/grpc-server/proto"
	"google.golang.org/grpc"
)

// Constantes de estado y configuración
const (
	StatusDisponible = "DISPONIBLE"
	StatusOcupado    = "OCUPADO"
	StatusCaido 	= "CAIDO"
	crashProbability = 0.1 // 10% de probabilidad de "caerse" después de una partida
	totalEntities    = 6   // [Jugador1, Jugador2, Matchmaker, Servidor1, Servidor2, Servidor3]
)

// gameServer implementa tanto la interfaz de servidor gRPC como la lógica de cliente.
type gameServer struct {
	pb.UnimplementedGameServerServer

	id               string
	serverIndex      int // Índice de este servidor en el reloj vectorial
	port             string
	status           string
	mu               sync.Mutex // Protege el acceso a 'status' y 'vectorClock'
	vectorClock      []int32
	matchmakerClient pb.MatchmakerClient
}

// incrementClock incrementa el reloj local para un nuevo evento.
func (s *gameServer) incrementClock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vectorClock[s.serverIndex]++
	log.Printf("[Server %s] Reloj incrementado: %v", s.id, s.vectorClock)
}

// mergeClocks fusiona el reloj local con uno remoto.
func (s *gameServer) mergeClocks(remoteClock []int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.vectorClock) != len(remoteClock) {
		return
	}
	for i := 0; i < len(s.vectorClock); i++ {
		if remoteClock[i] > s.vectorClock[i] {
			s.vectorClock[i] = remoteClock[i]
		}
	}
	log.Printf("[Server %s] Reloj fusionado: %v", s.id, s.vectorClock)
}

// ---- Responsabilidad: Informar al Matchmaker del estado ----
func (s *gameServer) updateStatusOnMatchmaker(newStatus string) error {
	s.incrementClock() // Un cambio de estado es un evento

	s.mu.Lock()
	s.status = newStatus
	// Construir la dirección completa con el puerto
	serverAddress := "10.35.168.121:50053"
	req := &pb.ServerStatusUpdateRequest{
		ServerId:    s.id,
		NewStatus:   s.status,
		Address:     serverAddress,
		VectorClock: s.vectorClock,
	}
	s.mu.Unlock()

	log.Printf("[Server %s] Notificando al Matchmaker nuevo estado: %s", s.id, newStatus)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	res, err := s.matchmakerClient.UpdateServerStatus(ctx, req)
	if err != nil {
		log.Printf("[Server %s] Error al actualizar estado en Matchmaker: %v", s.id, err)
		return err
	}

	// Al recibir la respuesta, actualizamos nuestro conocimiento del sistema.
	s.mergeClocks(res.GetVectorClock())
	return nil
}

// 1. Recibir asignación de partida
func (s *gameServer) AssignMatch(ctx context.Context, req *pb.AssignMatchRequest) (*pb.AssignMatchResponse, error) {
	log.Printf("[Server %s] Recibida solicitud de partida %s para jugadores %v", s.id, req.GetMatchId(), req.GetPlayerIds())

	s.mu.Lock()
	if s.status == StatusOcupado {
		s.mu.Unlock()
		log.Printf("[Server %s] Rechazando partida. Ya estoy ocupado.", s.id)
		return &pb.AssignMatchResponse{StatusCode: pb.AssignMatchResponse_REJECTED_BUSY}, nil
	}
	s.mu.Unlock()

	// Fusionamos el reloj del Matchmaker y luego incrementamos el nuestro por el evento de aceptación.
	s.mergeClocks(req.GetVectorClock())
	s.incrementClock()

	// 2. Simular ejecución de partida ----> Lanzamos la simulación en una goroutine para no bloquear la respuesta gRPC.
	go s.simulateMatch(req.GetMatchId(), req.GetPlayerIds())

	return &pb.AssignMatchResponse{StatusCode: pb.AssignMatchResponse_ACCEPTED}, nil
}

// Lógica de simulación de partida y actualización de estado post-partida.
func (s *gameServer) simulateMatch(matchId string, jugadores []string) {
	// 1. Informar que está ocupado
	s.status = StatusOcupado
	if err := s.updateStatusOnMatchmaker(StatusOcupado); err != nil {
		log.Printf("[Server %s] Falló al cambiar a OCUPADO. Abortando partida %s.", s.id, matchId)
		return
	}

	// 2. Simular la duración de la partida
	log.Printf("[Server %s] Partida %s de 5 segundos en curso...", s.id, matchId)
	time.Sleep(5 * time.Second)
	log.Printf("[Server %s] Partida %s finalizada.", s.id, matchId)

	// 3. Informar que está disponible nuevamente
	s.status = StatusDisponible
	if err := s.updateStatusOnMatchmaker(StatusDisponible); err != nil {
		// Si no puede contactar al matchmaker, puede que el matchmaker ya lo considere caído.
		log.Printf("[Server %s] No se pudo notificar al Matchmaker la disponibilidad. El servidor podría estar caido.", s.id)
	}

	req := &pb.MatchEndedRequest{
		MatchId:      matchId,
		PlayerIds:    jugadores,
		VectorClock:  s.vectorClock,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	_, err := s.matchmakerClient.NotifyMatchEnded(ctx, req)
	if err != nil {
		log.Printf("[Server %s] Error al notificar fin de partida al Matchmaker: %v", s.id, err)
		return
	}
}

func main() {
	serverId := "S3"
	serverIndex := 5
	port := "50053"
	// Conexión gRPC al Matchmaker (actuando como cliente)
	matchmakerAddr := "10.35.168.122:50051"
	conn, err := grpc.Dial(matchmakerAddr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("[Server %s] No se pudo conectar al Matchmaker: %v", serverId, err)
	}
	defer conn.Close()

	server := &gameServer{
		id:               serverId,
		serverIndex:      serverIndex,
		port:             port,
		status:           StatusDisponible, // Estado inicial
		vectorClock:      make([]int32, totalEntities),
		matchmakerClient: pb.NewMatchmakerClient(conn),
	}

	// Iniciar su propio servidor gRPC para escuchar al Matchmaker
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("[Server %s] Error al escuchar en el puerto %s: %v", serverId, port, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterGameServerServer(grpcServer, server)
	log.Printf("[Server %s] Escuchando en :50053", serverId)

	//4. Registrarse al iniciar ----> Lo hacemos en una goroutine para no bloquear el inicio del servidor gRPC si el Matchmaker no está listo aún.
	go func() {
		// Reintentar hasta que el registro sea exitoso
		for {
			err := server.updateStatusOnMatchmaker(StatusDisponible)
			if err == nil {
				log.Printf("[Server %s] Registrado exitosamente en el Matchmaker.", serverId)
				break
			}
			log.Printf("[Server %s] Falla al registrar. Reintentando en 5 segundos...", serverId)
			time.Sleep(5 * time.Second)
		}
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("[Server %s] Falla al servir gRPC: %v", serverId, err)
	}
}